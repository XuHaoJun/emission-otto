// Package emission provides an event emitter.
package emission

import (
	"errors"
	"fmt"
	"github.com/robertkrimen/otto"
	"os"
	"reflect"
	"sync"
)

// Default number of maximum listeners for an event.
const DefaultMaxListeners = 10

// Error presented when an invalid argument is provided as a listener function
var ErrNoneFunction = errors.New("Kind of Value for listener is not Func.")

type RecoveryListener func(interface{}, interface{}, error)

type Emitter struct {
	// Mutex to prevent race conditions within the Emitter.
	*sync.Mutex
	// Map of event to a slice of listener function's reflect Values.
	events     map[interface{}][]reflect.Value
	ottoEvents map[interface{}][]otto.Value
	// Optional RecoveryListener to call when a panic occurs.
	recoverer RecoveryListener
	// Maximum listeners for debugging potential memory leaks.
	maxListeners int
	//
	ottoVM *otto.Otto
}

// AddListener appends the listener argument to the event arguments slice
// in the Emitter's events map. If the number of listeners for an event
// is greater than the Emitter's maximum listeners then a warning is printed.
// If the relect Value of the listener does not have a Kind of Func then
// AddListener panics. If a RecoveryListener has been set then it is called
// recovering from the panic.
func (emitter *Emitter) AddListener(event, listener interface{}) *Emitter {
	emitter.Lock()
	defer emitter.Unlock()

	fn := reflect.ValueOf(listener)
	ottoFn, isOttoValue := listener.(otto.Value)

	if reflect.Func != fn.Kind() && isOttoValue && !ottoFn.IsFunction() {
		if nil == emitter.recoverer {
			panic(ErrNoneFunction)
		} else {
			emitter.recoverer(event, listener, ErrNoneFunction)
		}
	}

	if emitter.maxListeners != -1 && emitter.maxListeners < len(emitter.events[event])+1 {
		fmt.Fprintf(os.Stdout, "Warning: event `%v` has exceeded the maximum "+
			"number of listeners of %d.\n", event, emitter.maxListeners)
	}

	if isOttoValue {
		emitter.ottoEvents[event] = append(emitter.ottoEvents[event], ottoFn)
	} else {
		emitter.events[event] = append(emitter.events[event], fn)
	}

	return emitter
}

// On is an alias for AddListener.
func (emitter *Emitter) On(event, listener interface{}) *Emitter {
	return emitter.AddListener(event, listener)
}

// RemoveListener removes the listener argument from the event arguments slice
// in the Emitter's events map.  If the reflect Value of the listener does not
// have a Kind of Func then RemoveListener panics. If a RecoveryListener has
// been set then it is called after recovering from the panic.
func (emitter *Emitter) RemoveListener(event, listener interface{}) *Emitter {
	emitter.Lock()
	defer emitter.Unlock()

	fn := reflect.ValueOf(listener)
	ottoFn, isOttoValue := listener.(otto.Value)

	if reflect.Func != fn.Kind() && isOttoValue && !ottoFn.IsFunction() {
		if nil == emitter.recoverer {
			panic(ErrNoneFunction)
		} else {
			emitter.recoverer(event, listener, ErrNoneFunction)
		}
	}

	if isOttoValue {
		if events, ok := emitter.ottoEvents[event]; ok {
			for i, listener := range events {
				if ottoFn == listener {
					// Do not break here to ensure the listener has not been
					// added more than once.
					emitter.ottoEvents[event] = append(emitter.ottoEvents[event][:i], emitter.ottoEvents[event][i+1:]...)
				}
			}
		}
	} else {
		if events, ok := emitter.events[event]; ok {
			for i, listener := range events {
				if fn == listener {
					// Do not break here to ensure the listener has not been
					// added more than once.
					emitter.events[event] = append(emitter.events[event][:i], emitter.events[event][i+1:]...)
				}
			}
		}

	}

	return emitter
}

// Off is an alias for RemoveListener.
func (emitter *Emitter) Off(event, listener interface{}) *Emitter {
	return emitter.RemoveListener(event, listener)
}

// Once generates a new function which invokes the supplied listener
// only once before removing itself from the event's listener slice
// in the Emitter's events map. If the reflect Value of the listener
// does not have a Kind of Func then Once panics. If a RecoveryListener
// has been set then it is called after recovering from the panic.
func (emitter *Emitter) Once(event, listener interface{}) *Emitter {
	fn := reflect.ValueOf(listener)
	ottoFn, isOttoValue := listener.(otto.Value)

	if reflect.Func != fn.Kind() && isOttoValue && !ottoFn.IsFunction() {
		if nil == emitter.recoverer {
			panic(ErrNoneFunction)
		} else {
			emitter.recoverer(event, listener, ErrNoneFunction)
		}
	}

	var run func(...interface{})

	if isOttoValue {
		run = func(arguments ...interface{}) {
			defer emitter.RemoveListener(event, run)

			ottoFn.Call(otto.NullValue(), arguments...)
		}
	} else {
		run = func(arguments ...interface{}) {
			defer emitter.RemoveListener(event, run)

			var values []reflect.Value

			for i := 0; i < len(arguments); i++ {
				values = append(values, reflect.ValueOf(arguments[i]))
			}

			fn.Call(values)
		}
	}

	emitter.AddListener(event, run)
	return emitter
}

// Emit attempts to use the reflect package to Call each listener stored
// in the Emitter's events map with the supplied arguments. Each listener
// is called within its own go routine. The reflect package will panic if
// the agruments supplied do not align the parameters of a listener function.
// If a RecoveryListener has been set then it is called after recovering from
// the panic.
func (emitter *Emitter) Emit(event interface{}, arguments ...interface{}) *Emitter {
	var (
		listeners     []reflect.Value
		ottoListeners []otto.Value
		ok            bool
		ottoOk        bool
	)

	// Lock the mutex when reading from the Emitter's
	// events map.
	emitter.Lock()

	ottoListeners, ottoOk = emitter.ottoEvents[event]

	if listeners, ok = emitter.events[event]; !ok && !ottoOk {
		// If the Emitter does not include the event in its
		// event map, it has no listeners to Call yet.
		emitter.Unlock()
		return emitter
	}

	// Unlock the mutex immediately following the read
	// instead of deferring so that listeners registered
	// with Once can aquire the mutex for removal.
	emitter.Unlock()

	var wg sync.WaitGroup

	if ok {
		wg.Add(len(listeners))

		var values []reflect.Value

		for i := 0; i < len(arguments); i++ {
			values = append(values, reflect.ValueOf(arguments[i]))
		}

		for _, fn := range listeners {
			go func(fn reflect.Value) {
				// Recover from potential panics, supplying them to a
				// RecoveryListener if one has been set, else allowing
				// the panic to occur.
				if nil != emitter.recoverer {
					defer func() {
						if r := recover(); nil != r {
							err := errors.New(fmt.Sprintf("%v", r))
							emitter.recoverer(event, fn.Interface(), err)
						}
					}()
				}

				defer wg.Done()

				fn.Call(values)
			}(fn)
		}

		wg.Wait()
	}

	if ottoOk {
		wg.Add(len(ottoListeners))

		var values []interface{}

		for i := 0; i < len(arguments); i++ {
			v, err := emitter.ottoVM.ToValue(arguments[i])
			if err != nil {
				fmt.Println(err)
				return emitter
			}
			values = append(values, v)
		}

		for _, fn := range ottoListeners {
			go func(fn otto.Value) {
				// Recover from potential panics, supplying them to a
				// RecoveryListener if one has been set, else allowing
				// the panic to occur.
				if nil != emitter.recoverer {
					defer func() {
						if r := recover(); nil != r {
							err := errors.New(fmt.Sprintf("%v", r))
							inter, _ := fn.Export()
							emitter.recoverer(event, inter, err)
						}
					}()
				}

				defer wg.Done()

				fn.Call(otto.NullValue(), values...)
			}(fn)
		}

		wg.Wait()
	}
	return emitter
}

// RecoverWith sets the listener to call when a panic occurs, recovering from
// panics and attempting to keep the application from crashing.
func (emitter *Emitter) RecoverWith(listener RecoveryListener) *Emitter {
	emitter.recoverer = listener
	return emitter
}

// SetMaxListeners sets the maximum number of listeners per
// event for the Emitter. If -1 is passed as the maximum,
// all events may have unlimited listeners. By default, each
// event can have a maximum number of 10 listeners which is
// useful for finding memory leaks.
func (emitter *Emitter) SetMaxListeners(max int) *Emitter {
	emitter.Lock()
	defer emitter.Unlock()

	emitter.maxListeners = max
	return emitter
}

func (emitter *Emitter) ResetOttoEvents() *Emitter {
	emitter.ottoEvents = make(map[interface{}][]otto.Value)
	return emitter
}

// NewEmitter returns a new Emitter object, defaulting the
// number of maximum listeners per event to the DefaultMaxListeners
// constant and initializing its events map.
func NewEmitter() (emitter *Emitter) {
	emitter = new(Emitter)
	emitter.Mutex = new(sync.Mutex)
	emitter.events = make(map[interface{}][]reflect.Value)
	emitter.maxListeners = DefaultMaxListeners
	return
}

func NewEmitterOtto(vm *otto.Otto) (emitter *Emitter) {
	emitter = new(Emitter)
	emitter.Mutex = new(sync.Mutex)
	emitter.events = make(map[interface{}][]reflect.Value)
	emitter.ottoEvents = make(map[interface{}][]otto.Value)
	emitter.ottoVM = vm
	emitter.maxListeners = DefaultMaxListeners
	return
}
