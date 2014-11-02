package vm

import "github.com/joushou/gocnc/gcode"
import "math"
import "fmt"
import "errors"

//
// The CNC interpreter/"vm"
//
// It currently supports:
//
//   G00   - rapid move
//   G01   - linear move
//   G02   - cw arc
//   G03   - ccw arc
//   G17   - xy arc plane
//   G18   - xz arc plane
//   G19   - yz arc plane
//   G20   - imperial mode
//   G21   - metric mode
//   G80   - cancel mode (?)
//   G90   - absolute
//   G90.1 - absolute arc
//   G91   - relative
//   G91.1 - relative arc
//
//   M02 - end of program
//   M03 - spindle enable clockwise
//   M04 - spindle enable counterclockwise
//   M05 - spindle disable
//   M07 - mist coolant enable
//   M08 - flood coolant enable
//   M09 - coolant disable
//   M30 - end of program
//
//   F - feedrate
//   S - spindle speed
//   P - parameter
//   X, Y, Z - cartesian movement
//   I, J, K - arc center definition

//
// TODO
//
//   Handle multiple G/M codes on the same line (slice of pairs instead of map)
//   Split G/M handling out of the run function
//   Handle G/M-code priority properly
//   Better comments
//   Implement various canned cycles
//   Variables (basic support?)
//   Subroutines
//   Incremental axes
//   A, B, C axes

type Statement []*gcode.Word

func (stmt Statement) get(address rune) (res float64, err error) {
	found := false
	for _, m := range stmt {
		if m.Address == address {
			if found {
				return res, errors.New(fmt.Sprintf("Multiple instances of address '%c' in block", address))
			}
			found = true
			res = m.Command
		}
	}
	if !found {
		return res, errors.New(fmt.Sprintf("'%c' not found in block", address))
	}
	return res, nil
}

func (stmt Statement) getDefault(address rune, def float64) (res float64) {
	res, err := stmt.get(address)
	if err == nil {
		return def
	}
	return res
}

func (stmt Statement) getAll(address rune) (res []float64) {
	for _, m := range stmt {
		if m.Address == address {
			res = append(res, m.Command)
		}
	}
	return res
}

func (stmt Statement) includes(addresses ...rune) (res bool) {
	for _, m := range addresses {
		_, err := stmt.get(m)
		if err == nil {
			return true
		}
	}
	return false
}

func (stmt Statement) hasWord(address rune, command float64) (res bool) {
	for _, m := range stmt {
		if m.Address == address && m.Command == command {
			return true
		}
	}
	return false
}

//
// State structs
//

// Constants for move modes
const (
	moveModeNone   = iota
	moveModeRapid  = iota
	moveModeLinear = iota
	moveModeCWArc  = iota
	moveModeCCWArc = iota
)

// Constants for plane selection
const (
	planeXY = iota
	planeXZ = iota
	planeYZ = iota
)

// Move state
type State struct {
	feedrate         float64
	spindleSpeed     float64
	moveMode         int
	spindleEnabled   bool
	spindleClockwise bool
	floodCoolant     bool
	mistCoolant      bool
}

// Position and state
type Position struct {
	state   State
	x, y, z float64
}

// Machine state and settings
type Machine struct {
	state            State
	metric           bool
	absoluteMove     bool
	absoluteArc      bool
	movePlane        int
	completed        bool
	maxArcDeviation  float64
	minArcLineLength float64
	tolerance        float64
	posStack         []Position
}

//
// Positioning
//

// Retrieves position from top of stack
func (vm *Machine) curPos() Position {
	return vm.posStack[len(vm.posStack)-1]
}

// Appends a position to the stack
func (vm *Machine) addPos(pos Position) {
	vm.posStack = append(vm.posStack, pos)
}

// Calculates the absolute position of the given statement, including optional I, J, K parameters
func (vm *Machine) calcPos(stmt Statement) (newX, newY, newZ, newI, newJ, newK float64) {
	pos := vm.curPos()
	var err error

	if newX, err = stmt.get('X'); err != nil {
		newX = pos.x
	} else if !vm.metric {
		newX *= 25.4
	}

	if newY, err = stmt.get('Y'); err != nil {
		newY = pos.y
	} else if !vm.metric {
		newY *= 25.4
	}

	if newZ, err = stmt.get('Z'); err != nil {
		newZ = pos.z
	} else if !vm.metric {
		newZ *= 25.4
	}

	newI = stmt.getDefault('I', 0)
	newJ = stmt.getDefault('J', 0)
	newK = stmt.getDefault('K', 0)

	if !vm.metric {
		newI, newJ, newK = newI*25.4, newJ*25.4, newZ*25.4
	}

	if !vm.absoluteMove {
		newX, newY, newZ = pos.x+newX, pos.y+newY, pos.z+newZ
	}

	if !vm.absoluteArc {
		newI, newJ, newK = pos.x+newI, pos.y+newJ, pos.z+newK
	}
	return newX, newY, newZ, newI, newJ, newK
}

// Adds a simple linear move
func (vm *Machine) positioning(stmt Statement) {
	newX, newY, newZ, _, _, _ := vm.calcPos(stmt)
	vm.addPos(Position{vm.state, newX, newY, newZ})
}

// Calculates an approximate arc from the provided statement
func (vm *Machine) approximateArc(stmt Statement) {
	var (
		startPos                           Position = vm.curPos()
		endX, endY, endZ, endI, endJ, endK float64  = vm.calcPos(stmt)
		s1, s2, s3, e1, e2, e3, c1, c2     float64
		add                                func(x, y, z float64)
		clockwise                          bool = (vm.state.moveMode == moveModeCWArc)
	)

	vm.state.moveMode = moveModeLinear

	// Read the additional rotation parameter
	P := 0.0
	if pp, err := stmt.get('P'); err == nil {
		P = pp
	}

	//  Flip coordinate system for working in other planes
	switch vm.movePlane {
	case planeXY:
		s1, s2, s3, e1, e2, e3, c1, c2 = startPos.x, startPos.y, startPos.z, endX, endY, endZ, endI, endJ
		add = func(x, y, z float64) {
			wx, wy, wz := gcode.Word{'X', x}, gcode.Word{'Y', y}, gcode.Word{'Z', z}
			vm.positioning(Statement{&wx, &wy, &wz})
		}
	case planeXZ:
		s1, s2, s3, e1, e2, e3, c1, c2 = startPos.z, startPos.x, startPos.y, endZ, endX, endY, endK, endI
		add = func(x, y, z float64) {
			wx, wy, wz := gcode.Word{'X', y}, gcode.Word{'Y', z}, gcode.Word{'Z', x}
			vm.positioning(Statement{&wx, &wy, &wz})

		}
	case planeYZ:
		s1, s2, s3, e1, e2, e3, c1, c2 = startPos.y, startPos.z, startPos.x, endY, endZ, endX, endJ, endK
		add = func(x, y, z float64) {
			wx, wy, wz := gcode.Word{'X', z}, gcode.Word{'Y', x}, gcode.Word{'Z', y}
			vm.positioning(Statement{&wx, &wy, &wz})
		}
	}

	radius1 := math.Sqrt(math.Pow(c1-s1, 2) + math.Pow(c2-s2, 2))
	radius2 := math.Sqrt(math.Pow(c1-e1, 2) + math.Pow(c2-e2, 2))
	if radius1 == 0 || radius2 == 0 {
		panic("Invalid arc statement")
	}

	if math.Abs((radius2-radius1)/radius1) > 0.01 {
		panic(fmt.Sprintf("Radius deviation of %f percent", math.Abs((radius2-radius1)/radius1)*100))
	}

	theta1 := math.Atan2((s2 - c2), (s1 - c1))
	theta2 := math.Atan2((e2 - c2), (e1 - c1))

	angleDiff := theta2 - theta1
	if angleDiff < 0 && !clockwise {
		angleDiff += 2 * math.Pi
	} else if angleDiff > 0 && clockwise {
		angleDiff -= 2 * math.Pi
	}

	if clockwise {
		angleDiff -= P * 2 * math.Pi
	} else {
		angleDiff += P * 2 * math.Pi
	}

	steps := 1
	if vm.maxArcDeviation < radius1 {
		steps = int(math.Ceil(math.Abs(angleDiff / (2 * math.Acos(1-vm.maxArcDeviation/radius1)))))
	}

	// Enforce a minimum line length
	arcLen := math.Abs(angleDiff) * math.Sqrt(math.Pow(radius1, 2)+math.Pow((e3-s3)/angleDiff, 2))
	steps2 := int(arcLen / vm.minArcLineLength)

	if steps > steps2 {
		steps = steps2
	}

	angle := 0.0
	for i := 0; i <= steps; i++ {
		angle = theta1 + angleDiff/float64(steps)*float64(i)
		a1, a2 := c1+radius1*math.Cos(angle), c2+radius1*math.Sin(angle)
		a3 := s3 + (e3-s3)/float64(steps)*float64(i)
		add(a1, a2, a3)
	}
	add(e1, e2, e3)
}

//
// Dispatch
//

func (vm *Machine) run(stmt Statement) (err error) {
	if vm.completed {
		// A stop had previously been issued
		return
	}

	defer func() {
		if r := recover(); r != nil {
			err = errors.New(fmt.Sprintf("%s", r))
		}
	}()

	// G-codes
	for _, g := range stmt.getAll('G') {
		switch g {
		case 0:
			vm.state.moveMode = moveModeRapid
		case 1:
			vm.state.moveMode = moveModeLinear
		case 2:
			vm.state.moveMode = moveModeCWArc
		case 3:
			vm.state.moveMode = moveModeCCWArc
		case 17:
			vm.movePlane = planeXY
		case 18:
			vm.movePlane = planeXZ
		case 19:
			vm.movePlane = planeYZ
		case 20:
			vm.metric = false
		case 21:
			vm.metric = true
		case 80:
			vm.state.moveMode = moveModeNone
		case 90:
			vm.absoluteMove = true
		case 90.1:
			vm.absoluteArc = true
		case 91:
			vm.absoluteMove = false
		case 91.1:
			vm.absoluteArc = false
		}
	}

	// M-codes
	for _, m := range stmt.getAll('M') {
		switch m {
		case 2:
			vm.completed = true
		case 3:
			vm.state.spindleEnabled = true
			vm.state.spindleClockwise = true
		case 4:
			vm.state.spindleEnabled = true
			vm.state.spindleClockwise = false
		case 5:
			vm.state.spindleEnabled = false
		case 7:
			vm.state.mistCoolant = true
		case 8:
			vm.state.floodCoolant = true
		case 9:
			vm.state.mistCoolant = false
			vm.state.floodCoolant = false
		case 30:
			vm.completed = true
		}
	}

	// F-codes
	for _, f := range stmt.getAll('F') {
		if !vm.metric {
			f *= 25.4
		}
		if f <= 0 {
			return errors.New("Feedrate must be greater than zero")
		}
		vm.state.feedrate = f
	}

	// S-codes
	for _, s := range stmt.getAll('S') {
		if s < 0 {
			return errors.New("Spindle speed must be greater than or equal to zero")
		}
		vm.state.spindleSpeed = s
	}

	// X, Y, Z, I, J, K, P
	hasPositioning := stmt.includes('X', 'Y', 'Z')
	if hasPositioning {
		if vm.state.moveMode == moveModeCWArc || vm.state.moveMode == moveModeCCWArc {
			vm.approximateArc(stmt)
		} else if vm.state.moveMode == moveModeLinear || vm.state.moveMode == moveModeRapid {
			vm.positioning(stmt)
		} else {
			return errors.New("Move attempted without an active move mode")
		}
	}

	return nil
}

// Ensure that machine state is correct after execution
func (vm *Machine) finalize() {
	if vm.state != vm.curPos().state {
		vm.state.moveMode = moveModeNone
		vm.addPos(Position{state: vm.state})
	}
}

// Process AST
func (vm *Machine) Process(doc *gcode.Document) (err error) {
	for _, b := range doc.Blocks {
		if b.BlockDelete {
			continue
		}

		stmt := make(Statement, 0)
		for _, n := range b.Nodes {
			if word, ok := n.(*gcode.Word); ok {
				stmt = append(stmt, word)
			}
		}
		if err := vm.run(stmt); err != nil {
			return err
		}
	}
	vm.finalize()
	return
}

// Initialize the VM
// Suggested values are: Init(0.002, 0.01, 0.001)
func (vm *Machine) Init(maxArcDeviation, minArcLineLength, tolerance float64) {
	vm.posStack = append(vm.posStack, Position{})
	vm.metric = true
	vm.absoluteMove = true
	vm.absoluteArc = false
	vm.movePlane = planeXY
	vm.maxArcDeviation = maxArcDeviation
	vm.minArcLineLength = minArcLineLength
	vm.tolerance = tolerance
}
