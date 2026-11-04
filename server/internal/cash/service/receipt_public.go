package service

import (
	"encoding/json"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
)

// DecodeReceipt — receipt_data кассовой операции в обоих форматах (v2 и перенесённые чеки v1); nil — нет данных.
// Для чтения чеков вне кассы (API v1: GET /api/v1/sales, Transformers::sale()).
func DecodeReceipt(raw json.RawMessage) (*cash.Receipt, error) { return decodeReceipt(raw) }
