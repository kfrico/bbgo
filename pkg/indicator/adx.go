package indicator

import (
	"math"

	"github.com/c9s/bbgo/pkg/types"
)

//go:generate callbackgen -type ADX
type ADX struct {
	types.IntervalWindow
	sma                                  *SMA
	prevHigh, prevLow, prevClose         float64
	prevSmoothedTrueRange                float64
	prevSmoothedDirectionalMovementPlus  float64
	prevSmoothedDirectionalMovementMinus float64

	updateCallbacks []func(adx float64)
}

func NewAdx(intervalWindow types.IntervalWindow) *ADX {
	adx := &ADX{
		IntervalWindow: intervalWindow,
		sma: &SMA{
			IntervalWindow: intervalWindow,
		},
	}

	return adx
}

func (inc *ADX) Update(high, low, close float64) {
	TrueRange := math.Max(math.Max(high-low, math.Abs(high-inc.prevClose)), math.Abs(low-inc.prevClose))

	DirectionalMovementPlus := 0.

	if high-inc.prevHigh > inc.prevLow-low {
		DirectionalMovementPlus = math.Max(high-inc.prevHigh, 0)
	}

	DirectionalMovementMinus := 0.

	if inc.prevLow-low > high-inc.prevHigh {
		DirectionalMovementMinus = math.Max(inc.prevLow-low, 0)
	}

	SmoothedTrueRange := inc.prevSmoothedTrueRange - (inc.prevSmoothedTrueRange / float64(inc.Window)) + TrueRange

	SmoothedDirectionalMovementPlus := inc.prevSmoothedDirectionalMovementPlus - (inc.prevSmoothedDirectionalMovementPlus / float64(inc.Window)) + DirectionalMovementPlus

	SmoothedDirectionalMovementMinus := inc.prevSmoothedDirectionalMovementMinus - (inc.prevSmoothedDirectionalMovementMinus / float64(inc.Window)) + DirectionalMovementMinus

	DIPlus := SmoothedDirectionalMovementPlus / SmoothedTrueRange * 100
	DIMinus := SmoothedDirectionalMovementMinus / SmoothedTrueRange * 100
	DX := math.Abs(DIPlus-DIMinus) / (DIPlus + DIMinus) * 100

	inc.sma.Update(DX)

	inc.prevHigh = high
	inc.prevLow = low
	inc.prevClose = close
	inc.prevSmoothedTrueRange = SmoothedTrueRange
	inc.prevSmoothedDirectionalMovementPlus = SmoothedDirectionalMovementPlus
	inc.prevSmoothedDirectionalMovementMinus = SmoothedDirectionalMovementMinus
}

func (inc *ADX) Length() int {
	return inc.sma.Length()
}

func (inc *ADX) Last() float64 {
	if inc.sma == nil {
		return 0
	}

	return inc.sma.Last()
}

func (inc *ADX) Index(i int) float64 {
	if inc.sma == nil {
		return 0
	}

	return inc.sma.Index(i)
}

func (inc *ADX) PushK(k types.KLine) {
	inc.Update(k.High.Float64(), k.Low.Float64(), k.Close.Float64())
}

func (inc *ADX) CalculateAndUpdate(allKLines []types.KLine) {
	if inc.sma == nil {
		for _, k := range allKLines {
			inc.PushK(k)
		}
	} else {
		k := allKLines[len(allKLines)-1]
		inc.PushK(k)
	}

	inc.EmitUpdate(inc.sma.Last())
}

func (inc *ADX) handleKLineWindowUpdate(interval types.Interval, window types.KLineWindow) {
	if inc.Interval != interval {
		return
	}

	inc.CalculateAndUpdate(window)
}

func (inc *ADX) Bind(updater KLineWindowUpdater) {
	updater.OnKLineWindowUpdate(inc.handleKLineWindowUpdate)
}
