<?php

/*
 * Заполняет поле "php" в server/internal/billing/session/testdata/settle_oracle.json фактическим
 * результатом SessionSettlement::settle() старого Laravel (через eval.php, функция SessionSettlement.settle).
 *
 *   php fill-settle-fixture.php --app /путь/к/копии/Zafiro [--fixture путь/к/settle_oracle.json]
 *
 * Обычно запускается через `tools/php-oracle/run.sh settle`. Поле "go" и порядок сценариев не меняет.
 */

declare(strict_types=1);

define('ORACLE_LIBRARY', true);
$options = getopt('', ['app:', 'fixture:']);
$fixture = $options['fixture'] ?? __DIR__.'/../../server/internal/billing/session/testdata/settle_oracle.json';
$functions = require __DIR__.'/eval.php';

$data = json_decode((string) file_get_contents($fixture), true, flags: JSON_THROW_ON_ERROR);
$filled = 0;
foreach ($data['scenarios'] as &$scenario) {
    $result = $functions['SessionSettlement.settle']($scenario);
    $scenario['php'] = [
        'refund_main' => $result['refund_main'],
        'refund_bonus' => $result['refund_bonus'],
        'kept_minutes' => $result['kept_minutes'],
        'packages' => $result['packages'],
        'total_spent_decrease' => $result['total_spent_decrease'],
    ];
    $same = $scenario['php'] == $scenario['go'] ? '' : '  ≠ go';
    fwrite(STDERR, sprintf("%-28s main=%d bonus=%d kept=%d packages=%d spent-%d%s\n", $scenario['id'], $result['refund_main'], $result['refund_bonus'], $result['kept_minutes'], count($result['packages']), $result['total_spent_decrease'], $same));
    $filled++;
}
unset($scenario);
file_put_contents($fixture, json_encode($data, JSON_PRETTY_PRINT | JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_PRESERVE_ZERO_FRACTION)."\n");
fwrite(STDERR, "заполнено: $filled, PHP ".PHP_VERSION."\n");
