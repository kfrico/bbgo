package indicator

import (
	"math"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

/*
rsi implements Relative Strength Index (RSI)

https://www.investopedia.com/terms/r/rsi.asp
*/
//go:generate callbackgen -type RSI
type RSI struct {
	types.SeriesBase
	types.IntervalWindow
	Values          types.Float64Slice
	uRMA            *RMA
	dRMA            *RMA
	prevPrice       float64
	EndTime         time.Time
	updateCallbacks []func(value float64)
}

func (inc *RSI) Update(price float64) {
	if inc.uRMA == nil {
		inc.SeriesBase.Series = inc
		inc.uRMA = &RMA{IntervalWindow: inc.IntervalWindow}
		inc.dRMA = &RMA{IntervalWindow: inc.IntervalWindow}
	}

	if inc.prevPrice == 0 {
		inc.prevPrice = price
		return
	}

	inc.uRMA.Update(math.Max(price-inc.prevPrice, 0))
	inc.dRMA.Update(math.Max(inc.prevPrice-price, 0))

	rs := inc.uRMA.Last() / inc.dRMA.Last()
	rsi := 100 - (100 / (1 + rs))

	inc.Values.Push(rsi)

	inc.prevPrice = price
}

func (inc *RSI) Last() float64 {
	if len(inc.Values) == 0 {
		return 0.0
	}
	return inc.Values[len(inc.Values)-1]
}

func (inc *RSI) Index(i int) float64 {
	length := len(inc.Values)
	if length <= 0 || length-i-1 < 0 {
		return 0.0
	}
	return inc.Values[length-i-1]
}

func (inc *RSI) Length() int {
	return len(inc.Values)
}

var _ types.SeriesExtend = &RSI{}

func (inc *RSI) PushK(k types.KLine) {
	inc.Update(k.Close.Float64())
}

func (inc *RSI) CalculateAndUpdate(kLines []types.KLine) {
	for _, k := range kLines {
		if inc.EndTime != zeroTime && !k.EndTime.After(inc.EndTime) {
			continue
		}

		inc.PushK(k)
	}

	inc.EmitUpdate(inc.Last())
	inc.EndTime = kLines[len(kLines)-1].EndTime.Time()
}

func (inc *RSI) handleKLineWindowUpdate(interval types.Interval, window types.KLineWindow) {
	if inc.Interval != interval {
		return
	}

	inc.CalculateAndUpdate(window)
}

func (inc *RSI) Bind(updater KLineWindowUpdater) {
	updater.OnKLineWindowUpdate(inc.handleKLineWindowUpdate)
}
