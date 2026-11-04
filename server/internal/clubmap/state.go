// Package clubmap — карта клуба: зоны, карточки ПК, состояние ПК для панели, управление ПК из панели и события
// computer.state для WebSocket админки. Laravel: app/Http/Controllers/ClubZoneController.php,
// app/Http/Controllers/ClubComputerController.php, app/Services/ClubZoneComputerService.php,
// app/Services/ComputerPower.php, app/Services/AgentUpdate.php, app/Models/ClubComputer.php.
//
// Связь с агентом (в сети / нет, состояние агента, версия, возможности) — из присутствия в Valkey
// (agent/presence), а не из last_seen_at: heartbeat больше не пишется в Postgres на каждый пинг.
package clubmap

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// Computer — то, из чего строится состояние ПК (club_computers + присутствие).
type Computer struct {
	ID            int64
	Status        string // club_computers.status: available | online | offline | restarting
	IsReserved    bool
	BookedNow     bool // ожидающая бронь пересекает [сейчас, сейчас + booking_interval)
	LastSeenAt    *time.Time
	SessionID     *string
	SessionUserID *int64
	SessionGuest  bool
	SessionEndsAt *time.Time
	StaffUntil    *time.Time
	// Online — агент в сети по присутствию (presence.State.Online).
	Online bool
}

// HasPaidSession — ClubComputer::hasPaidSession(): сессия клиента (или гостя с кассы), конец впереди.
func (c Computer) HasPaidSession(now time.Time) bool {
	return c.SessionID != nil && (c.SessionUserID != nil || c.SessionGuest) && c.SessionEndsAt != nil && c.SessionEndsAt.After(now)
}

// HasStaffVisit — ClubComputer::hasStaffVisit(): ПК в руках сотрудника (staff_until впереди).
func (c Computer) HasStaffVisit(now time.Time) bool {
	return c.StaffUntil != nil && c.StaffUntil.After(now)
}

// Connection — ClubComputer::connectionStatus(): online, если агент в сети (присутствие).
func (c Computer) Connection() string {
	if c.Online {
		return "online"
	}
	return "offline"
}

// CurrentStatus — ClubComputer::currentStatus(): reserved (флаг или бронь сейчас) → offline (выключен из панели)
// → нет связи ни разу: available — offline, иначе статус колонки → в сети — online, иначе offline.
// Свежесть heartbeat (90 с) старого кода заменена присутствием агента.
func (c Computer) CurrentStatus() string {
	switch {
	case c.IsReserved || c.BookedNow:
		return "reserved"
	case c.Status == "offline":
		return "offline"
	case c.Online:
		return "online"
	case c.LastSeenAt == nil && c.Status != "available":
		return c.Status
	default:
		return "offline"
	}
}

// Light — подсветка ПК на карте (ClubZoneController::computerStates()): сотрудник → оплаченная сессия гостя или
// клиента → включён и свободен → без подсветки.
func (c Computer) Light(now time.Time) string {
	switch {
	case c.HasStaffVisit(now):
		return "pc-staff"
	case c.HasPaidSession(now):
		if c.SessionGuest {
			return "pc-guest"
		}
		return "pc-account"
	case c.CurrentStatus() == "online":
		return "pc-free"
	default:
		return ""
	}
}

// DisplayName — ClubComputer::displayName(): display_name ?: device_name ?: sprintf('PC-%02d', number).
func DisplayName(displayName, deviceName *string, number int64) string {
	switch {
	case displayName != nil && *displayName != "":
		return *displayName
	case deviceName != nil && *deviceName != "":
		return *deviceName
	default:
		return fmt.Sprintf("PC-%02d", number)
	}
}

// Hardware — железо ПК для строки подробностей.
type Hardware struct {
	MAC, LocalIP, RemoteIP, CPUModel, GPUModel, OSVersion *string
	CPUCores                                              *int32
	RAMTotalMB                                            *int64
}

// Details — $computerDetails шаблона club-map/index.blade.php: непустые части через « · »
// (MAC, LAN, WAN, CPU, ядра, RAM — number_format() с запятой тысяч, GPU, ОС).
func Details(h Hardware) string {
	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	if m := str(h.MAC); m != "" {
		add("MAC " + m)
	}
	if ip := str(h.LocalIP); ip != "" {
		add("LAN " + ip)
	}
	if ip := str(h.RemoteIP); ip != "" {
		add("WAN " + ip)
	}
	add(str(h.CPUModel))
	if h.CPUCores != nil && *h.CPUCores != 0 {
		add(strconv.FormatInt(int64(*h.CPUCores), 10) + " cores")
	}
	if h.RAMTotalMB != nil && *h.RAMTotalMB != 0 {
		add(numberFormat(*h.RAMTotalMB) + " MB RAM")
	}
	add(str(h.GPUModel))
	add(str(h.OSVersion))
	return strings.Join(parts, " · ")
}

// numberFormat — number_format($n) PHP: тысячи через запятую.
func numberFormat(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Версии агента (AgentUpdate)
// ---------------------------------------------------------------------------

var (
	versionPrefixRe = regexp.MustCompile(`^\d+\.\d+\.\d+`)
	versionExactRe  = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
)

// ValidLatest — AgentUpdate::latestVersion(): только «x.y.z».
func ValidLatest(v string) bool { return versionExactRe.MatchString(v) }

// IsOutdated — AgentUpdate::isOutdated(): нет объявленной версии или версии ПК — false; нечитаемая версия ПК
// (сборка вручную) — устаревшая; иначе version_compare(x.y.z ПК, latest) < 0.
func IsOutdated(version *string, latest string) bool {
	if latest == "" || version == nil {
		return false
	}
	m := versionPrefixRe.FindString(*version)
	if m == "" {
		return true
	}
	return compareVersions(m, latest) < 0
}

// compareVersions — version_compare() для «x.y.z» (числовые части).
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int64
		if i < len(pa) {
			x, _ = strconv.ParseInt(pa[i], 10, 64)
		}
		if i < len(pb) {
			y, _ = strconv.ParseInt(pb[i], 10, 64)
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

// PresenceByID — присутствие ПК клуба по id (только в сети).
func PresenceByID(states []presence.State) map[int64]presence.State {
	out := make(map[int64]presence.State, len(states))
	for _, st := range states {
		if st.Online {
			out[st.ComputerID] = st
		}
	}
	return out
}
