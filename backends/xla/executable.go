package xla

import (
	"github.com/gomlx/exceptions"
	"github.com/gomlx/gomlx/backends"
	"github.com/gomlx/gomlx/types/shapes"
	"github.com/gomlx/gomlx/types/xslices"
	"github.com/gomlx/gopjrt/pjrt"
	"github.com/gomlx/gopjrt/xlabuilder"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"
)

// Executable implements backends.Executable for XLA/PJRT github.com/gomlx/gopjrt
type Executable struct {
	backend         *Backend
	exec            *pjrt.LoadedExecutable
	name            string
	parameterNames  []string
	parameterShapes []shapes.Shape
	outputShapes    []shapes.Shape
}

func (b *Builder) Compile(outputs ...backends.Op) backends.Executable {
	if len(outputs) == 0 {
		exceptions.Panicf("backend %q, computation %q: you must have at least one output to a computation", BackendName, b.name)
	}
	xOutputs := make([]*xlabuilder.Op, len(outputs))
	outputShapes := make([]shapes.Shape, len(outputs))
	for ii, output := range outputs {
		xOutputs[ii] = castToXlaOp(output)
		outputShapes[ii] = xshapeToShape(xOutputs[ii].Shape)
	}

	// If there are more than 1 outputs, use a tuple output -- PJRT un-tuples them during execution..
	tupleOutput := xOutputs[0]
	if len(xOutputs) > 1 {
		var err error
		tupleOutput, err = xlabuilder.Tuple(xOutputs...)
		if err != nil {
			panic(errors.WithMessagef(err, "backend %q: failed to tuple the outputs to compile computation %q", BackendName, b.name))
		}
	}
	comp, err := b.builder.Build(tupleOutput)
	if err != nil {
		panic(errors.WithMessagef(err, "backend %q: failed to build HLO from computation %q", BackendName, b.name))
	}
	var exec *pjrt.LoadedExecutable
	if b.backend.supressLogging {
		pjrt.SuppressAbseilLoggingHack(func() {
			exec, err = b.backend.client.Compile().WithComputation(comp).Done()
		})
	} else {
		exec, err = b.backend.client.Compile().WithComputation(comp).Done()
	}
	if err != nil {
		panic(errors.WithMessagef(err, "backend %q: failed to compile computation %q", BackendName, b.name))
	}
	return &Executable{
		backend:         b.backend,
		exec:            exec,
		name:            b.name,
		parameterNames:  b.parameterNames,
		parameterShapes: b.parameterShapes,
		outputShapes:    outputShapes,
	}
}

// AssertValid panics if the backend or the executable are not ok -- e.g.: if they have been finalized or the builder
// has already been compiled.
func (e *Executable) AssertValid() {
	if e == nil || e.exec == nil || e.backend == nil {
		exceptions.Panicf("backend %q: Executable nil or already finalized", BackendName)
	}
	e.backend.AssertValid()
}

// Finalize immediately frees resources associated to the executable.
func (e *Executable) Finalize() {
	if e == nil || e.exec == nil || e.backend == nil {
		return
	}
	err := e.exec.Destroy()
	if err != nil {
		klog.Warningf("Error while destroying executable %q on backend %q: %+v", e.name, BackendName, err)
	}
	e.exec = nil
	e.backend = nil
	e.parameterNames = nil
	e.parameterShapes = nil
	e.outputShapes = nil
}

// Inputs returns the list of parameters names and shapes, in order created by the Builder.Parameter calls.
func (e *Executable) Inputs() (names []string, inputShapes []shapes.Shape) {
	return e.parameterNames, e.parameterShapes
}

// Outputs returns the list of the shapes of the outputs of the computation, in order given to the Builder.Compile call.
func (e *Executable) Outputs() (outputShapes []shapes.Shape) {
	return e.outputShapes
}

// Execute the executable on the default device (0). The number and shapes of the inputs must match those returned by Inputs.
func (e *Executable) Execute(inputs []backends.Buffer, donate []bool) []backends.Buffer {
	e.AssertValid()
	if len(inputs) != len(e.parameterShapes) {
		exceptions.Panicf("backend %q: wrong number of parameters to Execute %q: %d given, %d expected", BackendName, e.name, len(inputs), len(e.parameterShapes))
	}
	if len(donate) > 0 && len(donate) != len(e.parameterShapes) {
		exceptions.Panicf("backend %q: wrong number of donate values to Execute %q: %d given, nil or %d expected", BackendName, e.name, len(donate), len(e.parameterShapes))
	}
	pInputs := xslices.Map(inputs, castToPJRT)
	var pOutputs []*pjrt.Buffer
	var err error
	if len(donate) == 0 {
		pOutputs, err = e.exec.Execute(pInputs...).DonateNone().Done()
	} else {
		pOutputs, err = e.exec.Execute(pInputs...).SetDonate(donate).Done()
	}
	if err != nil {
		panic(errors.WithMessagef(err, "backend %q: failed to execute computation %q", BackendName, e.name))
	}
	return xslices.Map(pOutputs, func(e *pjrt.Buffer) backends.Buffer { return e })
}