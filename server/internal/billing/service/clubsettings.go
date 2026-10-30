package service

// Настройки клуба, которые нужны экранам панели (Этап 3): те же правила, что у расчётов.

// BookingGapMinutes — интервал между бронями клуба (ComputerReservations::gapMinutes()).
func BookingGapMinutes(settings []byte) int64 { return bookingGapMinutes(settings) }

// ClubCurrency — валюта клуба data_get(settings, 'finances.currency', 'KZT').
func ClubCurrency(settings []byte) string { return clubCurrency(settings) }
