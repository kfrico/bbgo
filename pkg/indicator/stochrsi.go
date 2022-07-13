package indicator

import (
	"github.com/c9s/bbgo/pkg/types"
)

//go:generate callbackgen -type STOCHRSI
type STOCHRSI struct {
	types.Interval
	K types.Float64Slice
	D types.Float64Slice

	kWindow     int
	dWindow     int
	rsiWindow   int
	stochWindow int

	rsi   *RSI
	stoch *STOCH

	ksma *SMA
	dsma *SMA

	updateCallbacks []func(k float64, d float64)
}

func NewStochrsi(kWindow, dWindow, rsiWindow, stochWindow int, interval types.Interval) *STOCHRSI {
	stochrsi := &STOCHRSI{
		Interval:    interval,
		kWindow:     kWindow,
		dWindow:     dWindow,
		rsiWindow:   rsiWindow,
		stochWindow: stochWindow,

		rsi: &RSI{
			IntervalWindow: types.IntervalWindow{Window: rsiWindow, Interval: interval},
		},
		stoch: &STOCH{
			IntervalWindow: types.IntervalWindow{Window: stochWindow, Interval: interval},
		},
		ksma: &SMA{
			IntervalWindow: types.IntervalWindow{Window: kWindow, Interval: interval},
		},
		dsma: &SMA{
			IntervalWindow: types.IntervalWindow{Window: dWindow, Interval: interval},
		},
	}

	return stochrsi
}

func (inc *STOCHRSI) Update(close float64) {
	inc.rsi.Update(close)
	inc.stoch.Update(inc.rsi.Last(), inc.rsi.Last(), inc.rsi.Last())
	inc.ksma.Update(inc.stoch.LastK())
	inc.dsma.Update(inc.ksma.Last())
	inc.K.Push(inc.ksma.Last())
	inc.D.Push(inc.dsma.Last())
}

func (inc *STOCHRSI) LastK() float64 {
	if len(inc.K) == 0 {
		return 0.0
	}
	return inc.K[len(inc.K)-1]
}

func (inc *STOCHRSI) LastD() float64 {
	if len(inc.K) == 0 {
		return 0.0
	}
	return inc.D[len(inc.D)-1]
}

func (inc *STOCHRSI) PushK(k types.KLine) {
	inc.Update(k.Close.Float64())
}

func (inc *STOCHRSI) CalculateAndUpdate(kLines []types.KLine) {
	if inc.rsi == nil {
		for _, k := range kLines {
			inc.PushK(k)
		}
	} else {
		k := kLines[len(kLines)-1]
		inc.PushK(k)
	}

	inc.EmitUpdate(inc.LastK(), inc.LastD())
}

func (inc *STOCHRSI) handleKLineWindowUpdate(interval types.Interval, window types.KLineWindow) {
	if inc.Interval != interval {
		return
	}

	inc.CalculateAndUpdate(window)
}

func (inc *STOCHRSI) Bind(updater KLineWindowUpdater) {
	updater.OnKLineWindowUpdate(inc.handleKLineWindowUpdate)
}

func (inc *STOCHRSI) GetD() types.Series {
	return &inc.D
}

func (inc *STOCHRSI) GetK() types.Series {
	return &inc.K
}
