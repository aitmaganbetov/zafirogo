//go:build loadgen

package main

import "math/rand/v2"

// Случайность генератора — расписание нагрузки и выбор операций, не криптография (ключи идемпотентности — uuid).

func rndFloat() float64       { return rand.Float64() }    // #nosec G404 -- расписание нагрузки
func rndExp() float64         { return rand.ExpFloat64() } // #nosec G404 -- расписание нагрузки
func rndIntN(n int) int       { return rand.IntN(n) }      // #nosec G404 -- расписание нагрузки
func rndInt64N(n int64) int64 { return rand.Int64N(n) }    // #nosec G404 -- расписание нагрузки
