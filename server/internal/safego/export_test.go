package safego

// Panics — сколько паник перехвачено с запуска процесса.
func Panics() uint64 { return panics.Load() }
