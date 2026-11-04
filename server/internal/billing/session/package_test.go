package session

import (
	"errors"
	"testing"
	"time"
)

// GAP-014 / BR-024 / GS-047 — запуск сохранённого пакета.
func TestStartPackage(t *testing.T) {
	free := Computer{ID: 4, ZoneID: 1, LastSeenAt: at(0)}
	tariff := &PackageTariff{InValidityPeriod: true}

	t.Run("GAP-014", func(t *testing.T) {
		// Пакет 90 мин, истекает через 40 мин 30 с → сессия 40 мин; пакет удаляется целиком.
		pkg := Package{ID: 1, UserID: customer, TariffID: i64(5), Name: "Пакет", MinutesLeft: 90, ExpiresAt: *at(40*time.Minute + 30*time.Second)}
		st, err := StartPackage(pkg, tariff, free, customer, t0)
		if err != nil {
			t.Fatal(err)
		}
		if st.Minutes != 40 || !st.DeletePackage || st.Extension.Extends || !st.Extension.EndsAt.Equal(t0.Add(40*time.Minute)) {
			t.Fatalf("старт = %+v", st)
		}
		if st.Part.Kind != KindTariff || st.Part.MainCents != 0 || st.Part.BonusCents != 0 || st.Part.Minutes != 40 || !st.Part.ExpiresAt.Equal(pkg.ExpiresAt) {
			t.Fatalf("часть = %+v", st.Part)
		}
		// Через 10 мин досрочное завершение: остаток 30 мин снова пакетом до того же срока.
		st.Part.ID = 1
		got, err := Settle([]Purchase{st.Part}, customer, &st.Extension.EndsAt, t0.Add(10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if got.KeptMinutes != 30 || len(got.Packages) != 1 || !got.Packages[0].ExpiresAt.Equal(pkg.ExpiresAt) || got.RefundMain+got.RefundBonus != 0 {
			t.Fatalf("завершение = %+v", got)
		}
		// Пакет с истёкшим сроком → «Срок абонемента истёк.»
		old := Package{ID: 2, UserID: customer, MinutesLeft: 60, ExpiresAt: *at(-time.Minute)}
		if _, err := StartPackage(old, tariff, free, customer, t0); !errors.Is(err, ErrPackageExpired) {
			t.Fatalf("истёкший пакет: %v", err)
		}
	})
	t.Run("BR-024/пример", func(t *testing.T) {
		pkg := Package{MinutesLeft: 90, ExpiresAt: *at(50*time.Minute + 30*time.Second)}
		st, err := StartPackage(pkg, nil, free, customer, t0)
		if err != nil || st.Minutes != 50 {
			t.Fatalf("= %+v, %v; want 50 мин", st, err)
		}
	})
	t.Run("GS-047", func(t *testing.T) {
		// Пакет 90 мин (срок — 30 дней) запускается целиком; конец ≈ now + 90 мин; второе завершение через 60 мин → 30 мин.
		expires := *at(43200 * time.Minute)
		now := t0.Add(30 * time.Minute)
		st, err := StartPackage(Package{MinutesLeft: 90, Name: "Пакет", ExpiresAt: expires}, tariff, free, customer, now)
		if err != nil || st.Minutes != 90 || !st.Extension.EndsAt.Equal(now.Add(90*time.Minute)) {
			t.Fatalf("= %+v, %v", st, err)
		}
		st.Part.ID = 2
		got, err := Settle([]Purchase{st.Part}, customer, &st.Extension.EndsAt, now.Add(60*time.Minute))
		if err != nil || got.KeptMinutes != 30 || !got.Packages[0].ExpiresAt.Equal(expires) {
			t.Fatalf("= %+v, %v", got, err)
		}
	})
	t.Run("меньше-минуты-до-срока", func(t *testing.T) {
		pkg := Package{MinutesLeft: 90, ExpiresAt: *at(59 * time.Second)}
		if _, err := StartPackage(pkg, tariff, free, customer, t0); !errors.Is(err, ErrPackageExpired) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("микросекунды-now", func(t *testing.T) {
		// getTimestamp(): 10:00:00.9 → 10:01:00 = 60 с → 1 мин.
		pkg := Package{MinutesLeft: 90, ExpiresAt: *at(time.Minute)}
		st, err := StartPackage(pkg, tariff, free, customer, t0.Add(900*time.Millisecond))
		if err != nil || st.Minutes != 1 {
			t.Fatalf("= %+v, %v", st, err)
		}
	})
	t.Run("порядок-проверок", func(t *testing.T) {
		pkg := Package{MinutesLeft: 30, ExpiresAt: *at(time.Hour)}
		reserved := free
		reserved.IsReserved = true
		if _, err := StartPackage(pkg, &PackageTariff{ZoneIDs: []int64{2}}, reserved, customer, t0); !errors.Is(err, ErrComputerReserved) {
			t.Errorf("сначала ПК: %v", err)
		}
		if _, err := StartPackage(pkg, &PackageTariff{ZoneIDs: []int64{2}}, free, customer, t0); !errors.Is(err, ErrPackageWrongZone) {
			t.Errorf("зона: %v", err)
		}
		if _, err := StartPackage(pkg, &PackageTariff{ZoneIDs: []int64{1}}, free, customer, t0); !errors.Is(err, ErrPackageOutsidePeriod) {
			t.Errorf("период: %v", err)
		}
		if _, err := StartPackage(pkg, nil, free, customer, t0); err != nil {
			t.Errorf("тариф удалён физически — без ограничений: %v", err)
		}
	})
	t.Run("продление-своей-сессии", func(t *testing.T) {
		pc := sessionPC(4, 1, i64(customer), at(20*time.Minute))
		st, err := StartPackage(Package{MinutesLeft: 30, ExpiresAt: *at(time.Hour)}, nil, pc, customer, t0)
		if err != nil || !st.Extension.Extends || !st.Extension.EndsAt.Equal(t0.Add(50*time.Minute)) {
			t.Fatalf("= %+v, %v", st, err)
		}
	})
	t.Run("список", func(t *testing.T) {
		if PackageListed(Package{ExpiresAt: t0}, t0) || !PackageListed(Package{ExpiresAt: t0.Add(time.Second)}, t0) {
			t.Error("список пакетов: expires_at > now")
		}
	})
}
