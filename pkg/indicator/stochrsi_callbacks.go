// Code generated by "callbackgen -type STOCHRSI"; DO NOT EDIT.

package indicator

import ()

func (inc *STOCHRSI) OnUpdate(cb func(k float64, d float64)) {
	inc.updateCallbacks = append(inc.updateCallbacks, cb)
}

func (inc *STOCHRSI) EmitUpdate(k float64, d float64) {
	for _, cb := range inc.updateCallbacks {
		cb(k, d)
	}
}
