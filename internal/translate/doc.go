// Package translate defines the translation provider contract and the V1
// default HTTP NMT adapter.
//
// V1 routes every non-cached segment through a single neural machine
// translation endpoint over HTTP. The adapter sends source text (with
// placeholder tokens intact) and expects the provider to return translated
// text preserving those tokens.
package translate
