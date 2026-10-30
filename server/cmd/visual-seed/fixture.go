package main

import "encoding/json"

// Fixture — tools/visual/fixtures/demo.json. Суммы — строки тенге, время — RFC 3339 (местное время клуба).
type Fixture struct {
	Now   string `json:"now"`
	Login struct {
		Password       string           `json:"password"`
		PasswordBcrypt string           `json:"password_bcrypt"`
		Roles          map[string]int64 `json:"roles"`
	} `json:"login"`
	SaasPlans         []SaasPlan        `json:"saas_plans"`
	Clubs             []Club            `json:"clubs"`
	Users             []User            `json:"users"`
	Memberships       []Membership      `json:"memberships"`
	Zones             []Zone            `json:"zones"`
	Computers         []Computer        `json:"computers"`
	Tariffs           []Tariff          `json:"tariffs"`
	ProductCategories []ProductCategory `json:"product_categories"`
	Products          []Product         `json:"products"`
	Services          []Service         `json:"services"`
	Operations        []Operation       `json:"operations"`
	ShopOrders        []ShopOrder       `json:"shop_orders"`
	Reservations      []Reservation     `json:"reservations"`
	News              []News            `json:"news"`
	TodoTasks         []TodoTask        `json:"todo_tasks"`
	ChatMessages      []ChatMessage     `json:"chat_messages"`
	ActivityLogs      []ActivityLog     `json:"activity_logs"`
	QrLogins          []QrLogin         `json:"qr_logins"`
}

type SaasPlan struct {
	ID           int64   `json:"id"`
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	PriceMonthly *string `json:"price_monthly"`
	TrialDays    int64   `json:"trial_days"`
	IsActive     bool    `json:"is_active"`
}

type Club struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	Slug         string          `json:"slug"`
	Timezone     string          `json:"timezone"`
	Locale       string          `json:"locale"`
	Settings     json.RawMessage `json:"settings"`
	CreatedAt    string          `json:"created_at"`
	Subscription struct {
		SaasPlanID          int64   `json:"saas_plan_id"`
		Status              string  `json:"status"`
		StartedAt           string  `json:"started_at"`
		TrialEndsAt         *string `json:"trial_ends_at"`
		CurrentPeriodEndsAt *string `json:"current_period_ends_at"`
	} `json:"subscription"`
}

type User struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Phone        string  `json:"phone"`
	Email        *string `json:"email"`
	IsSuperadmin bool    `json:"is_superadmin"`
	BlockedAt    *string `json:"blocked_at"`
	CreatedAt    string  `json:"created_at"`
}

type Membership struct {
	ClubID                int64           `json:"club_id"`
	UserID                int64           `json:"user_id"`
	Role                  string          `json:"role"`
	Balance               *string         `json:"balance"`
	BonusBalance          *string         `json:"bonus_balance"`
	Discount              *string         `json:"discount"`
	TotalSpent            *string         `json:"total_spent"`
	CanManageIntegrations bool            `json:"can_manage_integrations"`
	Permissions           json.RawMessage `json:"permissions"`
}

type Zone struct {
	ID           int64   `json:"id"`
	ClubID       int64   `json:"club_id"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	Capacity     *int64  `json:"capacity"`
	Icon         string  `json:"icon"`
	Theme        string  `json:"theme"`
	Status       string  `json:"status"`
	IsFeatured   bool    `json:"is_featured"`
	SortOrder    int64   `json:"sort_order"`
	PositionX    int64   `json:"position_x"`
	PositionY    int64   `json:"position_y"`
	Width        int64   `json:"width"`
	Height       int64   `json:"height"`
	Rotation     int64   `json:"rotation"`
	PricePerHour *string `json:"price_per_hour"`
}

type Computer struct {
	ID                   int64   `json:"id"`
	ClubID               int64   `json:"club_id"`
	ZoneID               int64   `json:"zone_id"`
	Number               int64   `json:"number"`
	Status               *string `json:"status"`
	IsReserved           bool    `json:"is_reserved"`
	PositionX            int64   `json:"position_x"`
	PositionY            int64   `json:"position_y"`
	DeviceName           *string `json:"device_name"`
	DisplayName          *string `json:"display_name"`
	LastSeenAt           *string `json:"last_seen_at"`
	AgentState           *string `json:"agent_state"`
	AgentVersion         *string `json:"agent_version"`
	CurrentSessionID     *string `json:"current_session_id"`
	CurrentSessionUserID *int64  `json:"current_session_user_id"`
	SessionGuest         bool    `json:"session_guest"`
	SessionEndsAt        *string `json:"session_ends_at"`
	MacAddress           *string `json:"mac_address"`
	LocalIP              *string `json:"local_ip"`
	CPUModel             *string `json:"cpu_model"`
	CPUCores             *int64  `json:"cpu_cores"`
	RAMTotalMB           *int64  `json:"ram_total_mb"`
	GPUModel             *string `json:"gpu_model"`
	OSVersion            *string `json:"os_version"`
	HardwareReportedAt   *string `json:"hardware_reported_at"`
}

type Tariff struct {
	ID                   int64           `json:"id"`
	ClubID               int64           `json:"club_id"`
	Name                 string          `json:"name"`
	Description          *string         `json:"description"`
	DurationMinutes      int64           `json:"duration_minutes"`
	Price                string          `json:"price"`
	IsActive             *bool           `json:"is_active"`
	PaymentMethod        string          `json:"payment_method"`
	DiscountEnabled      bool            `json:"discount_enabled"`
	UserRoles            json.RawMessage `json:"user_roles"`
	UsageLimitEnabled    bool            `json:"usage_limit_enabled"`
	UsageLimitMinutes    *int64          `json:"usage_limit_minutes"`
	QuantityLimitEnabled bool            `json:"quantity_limit_enabled"`
	QuantityLimit        *int64          `json:"quantity_limit"`
	ValidityPeriods      json.RawMessage `json:"validity_periods"`
	SalePeriods          json.RawMessage `json:"sale_periods"`
	ZoneIDs              []int64         `json:"zone_ids"`
}

type ProductCategory struct {
	ID     int64  `json:"id"`
	ClubID int64  `json:"club_id"`
	Name   string `json:"name"`
}

type Product struct {
	ID         int64  `json:"id"`
	ClubID     int64  `json:"club_id"`
	CategoryID int64  `json:"category_id"`
	SKU        string `json:"sku"`
	Name       string `json:"name"`
	Price      string `json:"price"`
	Quantity   int64  `json:"quantity"`
}

type Service struct {
	ID       int64   `json:"id"`
	ClubID   int64   `json:"club_id"`
	Name     string  `json:"name"`
	Price    string  `json:"price"`
	Unit     *string `json:"unit"`
	IsActive *bool   `json:"is_active"`
}

// Operation — кассовая операция: open_shift, sale, top_up, transaction, close_shift.
type Operation struct {
	At             string `json:"at"`
	Op             string `json:"op"`
	ClubID         int64  `json:"club_id"`
	UserID         int64  `json:"user_id"`
	OpeningBalance string `json:"opening_balance"`
	Items          []struct {
		ProductID int64 `json:"product_id"`
		ServiceID int64 `json:"service_id"`
		Quantity  int64 `json:"quantity"`
	} `json:"items"`
	CustomerID    int64  `json:"customer_id"`
	Amount        string `json:"amount"`
	BalanceType   string `json:"balance_type"`
	PaymentMethod string `json:"payment_method"`
	Type          string `json:"type"`
	Description   string `json:"description"`
}

type ShopOrder struct {
	ID           int64  `json:"id"`
	ClubID       int64  `json:"club_id"`
	UserID       int64  `json:"user_id"`
	ComputerID   *int64 `json:"computer_id"`
	ComputerName string `json:"computer_name"`
	Items        []struct {
		ProductID int64  `json:"product_id"`
		Name      string `json:"name"`
		Quantity  int64  `json:"quantity"`
		Price     string `json:"price"`
	} `json:"items"`
	Amount    string  `json:"amount"`
	Status    string  `json:"status"`
	HandledBy *int64  `json:"handled_by"`
	HandledAt *string `json:"handled_at"`
	CreatedAt string  `json:"created_at"`
}

type Reservation struct {
	ID            int64   `json:"id"`
	ClubID        int64   `json:"club_id"`
	ComputerID    int64   `json:"computer_id"`
	UserID        *int64  `json:"user_id"`
	CustomerName  *string `json:"customer_name"`
	CustomerPhone *string `json:"customer_phone"`
	Note          *string `json:"note"`
	StartsAt      string  `json:"starts_at"`
	EndsAt        string  `json:"ends_at"`
	ArrivedAt     *string `json:"arrived_at"`
	CancelledAt   *string `json:"cancelled_at"` //nolint:misspell // колонка старой схемы
	CreatedBy     *int64  `json:"created_by"`
	CreatedAt     string  `json:"created_at"`
}

type News struct {
	ID          int64  `json:"id"`
	ClubID      *int64 `json:"club_id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	IsPublished bool   `json:"is_published"`
	CreatedBy   *int64 `json:"created_by"`
	CreatedAt   string `json:"created_at"`
}

type TodoTask struct {
	ID        int64   `json:"id"`
	ClubID    int64   `json:"club_id"`
	Title     string  `json:"title"`
	DueOn     string  `json:"due_on"`
	CreatedBy *int64  `json:"created_by"`
	DoneAt    *string `json:"done_at"`
	DoneBy    *int64  `json:"done_by"`
	CreatedAt string  `json:"created_at"`
}

type ChatMessage struct {
	ID         int64   `json:"id"`
	ClubID     int64   `json:"club_id"`
	CustomerID int64   `json:"customer_id"`
	AuthorID   *int64  `json:"author_id"`
	FromStaff  bool    `json:"from_staff"`
	Body       string  `json:"body"`
	ComputerID *int64  `json:"computer_id"`
	ReadAt     *string `json:"read_at"`
	CreatedAt  string  `json:"created_at"`
}

type ActivityLog struct {
	ClubID        int64           `json:"club_id"`
	Event         string          `json:"event"`
	Description   string          `json:"description"`
	UserID        int64           `json:"user_id"`
	SubjectUserID *int64          `json:"subject_user_id"`
	Details       json.RawMessage `json:"details"`
	CreatedAt     string          `json:"created_at"`
}

// QrLogin — открытый QR-вход на ПК (страница /pc-login/{code}): код хранится хэшем sha256, как AgentQrLogin::hashCode().
type QrLogin struct {
	ID         string `json:"id"`
	ClubID     int64  `json:"club_id"`
	ComputerID int64  `json:"computer_id"`
	Code       string `json:"code"`
	ExpiresAt  string `json:"expires_at"`
	CreatedAt  string `json:"created_at"`
}
