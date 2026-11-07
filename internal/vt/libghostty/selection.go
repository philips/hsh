//go:build goexperiment.simd

package libghostty

type Selection struct {
	Start     GridRef
	End       GridRef
	Rectangle bool
}
