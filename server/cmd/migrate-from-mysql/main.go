// Команда migrate-from-mysql — заготовка поклубного переноса данных из MySQL (Laravel Zafiro)
// в PostgreSQL (zafirogo). Описание будущего процесса: tools/migrate-from-mysql/README.md.
//
// Сейчас (Этап 0) только печатает план и ничего не подключает и не пишет.
package main

import (
	"flag"
	"fmt"
	"os"
)

var plan = []string{
	"1. Заморозить клуб в Laravel (режим только чтения для club_id).",
	"2. Прочитать данные клуба из MySQL (только WHERE club_id = ?), в одной транзакции REPEATABLE READ.",
	"3. Преобразовать: деньги → int64 тиыны, даты → timestamptz (UTC), enum/статусы → новые значения.",
	"4. Записать в PostgreSQL в одной транзакции; первичные ключи — новые, таблица соответствия id сохраняется.",
	"5. Перенести баланс через журнал (ledger): стартовая проводка «перенос из Laravel» с ключом идемпотентности.",
	"6. Сверка: количества строк, суммы балансов и кассы до тиына, контрольные выборки.",
	"7. Переключить клуб на zafirogo (агенты ПК переустанавливаются); Laravel-клуб оставить только для чтения.",
}

func main() {
	club := flag.Int64("club", 0, "id клуба в Laravel (MySQL), который переносим")
	dryRun := flag.Bool("dry-run", true, "только показать план (пока единственный режим)")
	flag.Parse()

	fmt.Println("migrate-from-mysql: заготовка (Этап 0), перенос ещё не реализован.")
	if *club > 0 {
		fmt.Printf("Клуб: %d, dry-run: %v\n", *club, *dryRun)
	}
	fmt.Println("План поклубного переноса:")
	for _, step := range plan {
		fmt.Println("  " + step)
	}
	if !*dryRun {
		fmt.Fprintln(os.Stderr, "режим записи не реализован")
		os.Exit(2)
	}
}
