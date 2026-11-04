package clubmap

import (
	"encoding/json"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// computer.load несёт температуры CPU/GPU для живого графика панели ПК (Этап 7); без здоровья — полей нет (старое тело).
func TestLoadDataTemperatures(t *testing.T) {
	st := presence.State{ComputerID: 7, HasLoad: true, Load: presence.Load{CPUBasisPoints: 4200, RAMBasisPoints: 6100},
		Health: &presence.Health{CPUTempDeci: 655, GPUTempDeci: 712}}
	b, _ := json.Marshal(LoadDataOf(st))
	if got, want := string(b), `{"computer_id":7,"load":{"cpu_bp":4200,"ram_bp":6100,"idle_seconds":0},"problems":[],"cpu_temp_deci":655,"gpu_temp_deci":712}`; got != want {
		t.Errorf("with health:\n got %s\nwant %s", got, want)
	}
	st.Health = nil
	b, _ = json.Marshal(LoadDataOf(st))
	if got, want := string(b), `{"computer_id":7,"load":{"cpu_bp":4200,"ram_bp":6100,"idle_seconds":0},"problems":[]}`; got != want {
		t.Errorf("without health:\n got %s\nwant %s", got, want)
	}
}
