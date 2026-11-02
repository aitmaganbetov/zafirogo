package v1

// RouteInfo — маршрут для тестов пакета v1_test.
type RouteInfo struct{ Method, Path, Name, Scope string }

// Routes — routes/api_v1.php в виде, удобном тестам.
func Routes() []RouteInfo {
	out := make([]RouteInfo, len(routes))
	for i, r := range routes {
		out[i] = RouteInfo{r.method, r.path, r.name, r.scope}
	}
	return out
}

// Exported for tests.
var (
	MaskPhone    = maskPhone
	EncodeCursor = encodeCursor
	DecodeCursor = decodeCursor
)
