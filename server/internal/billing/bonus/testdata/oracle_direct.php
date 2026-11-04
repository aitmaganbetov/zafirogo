<?php

/*
 * Прямой PHP-оракул для пакета bonus (go test -tags=phporacle, ZAFIRO_ORACLE_MODE=direct).
 *
 *   php oracle_direct.php <копия Zafiro с vendor/> < calls.jsonl > results.jsonl
 *
 * В отличие от tools/php-oracle/eval.php не поднимает Laravel (нет БД и контейнера), поэтому работает
 * на PHP 8.3 (как money.PHPRound): composer.lock старого проекта требует PHP >= 8.4, но
 * PercentLevels / TopUpBonus / LoyaltyProgram и нужные части illuminate/* исполняются и на 8.3.
 * Исполняется НАСТОЯЩИЙ код старого проекта: app/Support/PercentLevels.php, app/Services/TopUpBonus.php,
 * app/Services/LoyaltyProgram.php, app/Services/AgentTariffPurchaseService.php::amountToCents().
 * Настройки клуба приходят сырым JSON и декодируются как Eloquent-каст 'array' (json_decode($s, true)),
 * CurrentClub::settings() подменён (null → []), всё остальное — без изменений.
 *
 * Вход: {"id": …, "fn": "normalize|topup|loyalty|levels", "args": {…}} по строке; выход — {"id", "ok", "result"|"error"}.
 */

declare(strict_types=1);

$app = rtrim($argv[1] ?? '', '/');
if ($app === '' || ! is_file($app.'/vendor/composer/ClassLoader.php')) {
    fwrite(STDERR, "oracle_direct.php: укажите копию Zafiro с vendor/\n");
    exit(2);
}

// Автозагрузка без vendor/autoload.php: тот проверяет платформу (PHP >= 8.4) и падает на 8.3.
require $app.'/vendor/composer/ClassLoader.php';
$loader = new Composer\Autoload\ClassLoader();
foreach (require $app.'/vendor/composer/autoload_psr4.php' as $ns => $dirs) {
    $loader->setPsr4($ns, $dirs);
}
$loader->addClassMap(require $app.'/vendor/composer/autoload_classmap.php');
$loader->setPsr4('App\\', [$app.'/app']);
$loader->register();
foreach (require $app.'/vendor/composer/autoload_files.php' as $file) {
    // Только полифилы и хелперы illuminate/*: тестовые пакеты (Pest, PHPUnit, Collision) написаны под PHP 8.4.
    if (str_contains($file, '/symfony/polyfill-') || str_contains($file, '/laravel/framework/src/Illuminate/')) {
        require_once $file;
    }
}

use App\Models\ClubMembership;
use App\Services\CurrentClub;
use App\Services\LoyaltyProgram;
use App\Services\TopUpBonus;
use App\Support\PercentLevels;

final class OracleClub extends CurrentClub
{
    public mixed $raw = null;

    /** CurrentClub::settings(): settings клуба после каста 'array'; null → []. */
    public function settings(): array
    {
        return $this->raw ?? [];
    }
}

$club = new OracleClub();
$withSettings = function (array $a) use ($club): void {
    $club->raw = json_decode((string) ($a['settings'] ?? 'null'), true);
};

$functions = [
    // PercentLevels::normalize(json_decode(levels), threshold)
    'normalize' => fn (array $a): array => PercentLevels::normalize(json_decode((string) $a['levels'], true), (string) $a['threshold']),
    // TopUpBonus::for((float) amount) при settings
    'topup' => function (array $a) use ($withSettings, $club): array {
        $withSettings($a);

        return (new TopUpBonus($club))->for((float) $a['amount']);
    },
    // LoyaltyProgram::percentFor / discountFor для club_memberships {total_spent, discount} (decimal:2 — строки)
    'loyalty' => function (array $a) use ($withSettings, $club): array {
        $withSettings($a);
        $loyalty = new LoyaltyProgram($club);
        $membership = (new ClubMembership())->forceFill(['total_spent' => $a['total_spent'], 'discount' => $a['discount']]);

        return ['percent' => $loyalty->percentFor($membership), 'discount' => $loyalty->discountFor($membership)];
    },
    // Уровни в силе: TopUpBonus::levels(), LoyaltyProgram::levels()
    'levels' => function (array $a) use ($withSettings, $club): array {
        $withSettings($a);

        return ['topup' => (new TopUpBonus($club))->levels(), 'loyalty' => (new LoyaltyProgram($club))->levels()];
    },
    'version' => fn (array $a): string => PHP_VERSION,
];

$flags = JSON_UNESCAPED_UNICODE | JSON_PRESERVE_ZERO_FRACTION | JSON_UNESCAPED_SLASHES;
while (($line = fgets(STDIN)) !== false) {
    $call = json_decode($line, true);
    $out = ['id' => $call['id'] ?? null];
    try {
        $out += ['ok' => true, 'result' => $functions[$call['fn']]($call['args'] ?? [])];
    } catch (Throwable $e) {
        $out += ['ok' => false, 'error' => $e::class.': '.$e->getMessage()];
    }
    $json = json_encode($out, $flags);
    echo ($json === false ? json_encode(['id' => $out['id'], 'ok' => false, 'error' => 'json_encode: '.json_last_error_msg()]) : $json)."\n";
}
