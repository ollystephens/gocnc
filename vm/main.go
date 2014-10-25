package vm

import "github.com/joushou/gocnc/gcode"

//
// The CNC interpreter/"vm"
//
// It currently supports:
//
//   G00 - rapid move
//   G01 - linear move
//   G02 - cw arc
//   G03 - ccw arc
//   G20 - imperial mode
//   G21 - metric mode
//   G80 - cancel mode (?)
//   G90 - absolute
//   G91 - relative
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

type Statement map[rune]float64

//
// State structs
//

const (
	moveModeInitial = iota
	moveModeRapid   = iota
	moveModeLinear  = iota
	moveModeCWArc   = iota
	moveModeCCWArc  = iota
)

const (
	planeXY = iota
	planeXZ = iota
	planeYZ = iota
)

type State struct {
	feedrate         float64
	spindleSpeed     float64
	moveMode         int
	movePlane        int
	spindleEnabled   bool
	spindleClockwise bool
	floodCoolant     bool
	mistCoolant      bool
}

func (s *State) Equal(o *State) bool {
	return (s.feedrate == o.feedrate &&
		s.spindleSpeed == o.spindleSpeed &&
		s.moveMode == o.moveMode &&
		s.movePlane == o.movePlane &&
		s.spindleEnabled == o.spindleEnabled &&
		s.spindleClockwise == o.spindleClockwise &&
		s.floodCoolant == o.floodCoolant &&
		s.mistCoolant == o.mistCoolant)
}

type Position struct {
	state   State
	x, y, z float64
	i, j, k float64
	rot     int64
}

type Machine struct {
	state     State
	mode      string
	metric    bool
	absolute  bool
	completed bool
	posStack  []Position
}

//
// Positioning
//
func positioning(stmt Statement, state State, pos Position, metric, absolute bool) Position {
	var (
		newX, newY, newZ, newI, newJ, newK float64
		rot                                int64
		ok                                 bool
	)
	if newX, ok = stmt['X']; !ok {
		newX = pos.x
	} else if !metric {
		newX *= 25.4
	}

	if newY, ok = stmt['Y']; !ok {
		newY = pos.y
	} else if !metric {
		newY *= 25.4
	}

	if newZ, ok = stmt['Z']; !ok {
		newZ = pos.z
	} else if !metric {
		newZ *= 25.4
	}

	newI = stmt['I']
	newJ = stmt['J']
	newK = stmt['K']

	if !metric {
		newI, newJ, newK = newI*25.4, newJ*25.4, newK*25.4
	}

	rot = int64(stmt['P'])
	if rot == 0 {
		rot = 1
	}

	if !absolute {
		newX, newY, newZ = newX+pos.x, newY+pos.y, newZ+pos.z
	}
	return Position{state, newX, newY, newZ, newI, newJ, newK, rot}
}

//
// Dispatch
//
func (vm *Machine) run(stmt Statement) {
	if vm.completed {
		// A stop had previously been issued
		return
	}

	// G-codes
	if g, ok := stmt['G']; ok {
		switch g {
		case 0:
			vm.mode = "positioning"
			vm.state.moveMode = moveModeRapid
		case 1:
			vm.mode = "positioning"
			vm.state.moveMode = moveModeLinear
		case 2:
			vm.mode = "positioning"
			vm.state.moveMode = moveModeCWArc
		case 3:
			vm.mode = "positioning"
			vm.state.moveMode = moveModeCCWArc
		case 17:
			vm.state.movePlane = planeXY
		case 18:
			vm.state.movePlane = planeXZ
		case 19:
			vm.state.movePlane = planeYZ
		case 20:
			vm.metric = false
		case 21:
			vm.metric = true
		case 80:
			vm.mode = "cancelled"
		case 90:
			vm.absolute = true
		case 91:
			vm.absolute = false
		}
	}

	// M-codes
	if g, ok := stmt['M']; ok {
		switch g {
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
	if g, ok := stmt['F']; ok {
		if !vm.metric {
			g *= 25.4
		}
		vm.state.feedrate = g
	}

	// S-codes
	if g, ok := stmt['S']; ok {
		vm.state.spindleSpeed = g
	}

	// X, Y, Z, I, J, K, P
	_, hasX := stmt['X']
	_, hasY := stmt['Y']
	_, hasZ := stmt['Z']
	if (hasX || hasY || hasZ) && vm.mode == "positioning" {
		pos := positioning(stmt, vm.state, vm.posStack[len(vm.posStack)-1], vm.metric, vm.absolute)
		vm.posStack = append(vm.posStack, pos)
	}
}

//
// Initialize VM state
//
func (vm *Machine) Init() {
	vm.posStack = append(vm.posStack, Position{})
	vm.metric = true
	vm.absolute = true
}

//
// Process an AST
//
func (vm *Machine) Process(doc *gcode.Document) {
	for _, b := range doc.Blocks {
		if b.BlockDelete {
			continue
		}

		stmt := make(Statement)
		for _, n := range b.Nodes {
			if word, ok := n.(*gcode.Word); ok {
				stmt[word.Address] = word.Command
			}
		}
		vm.run(stmt)
	}
}