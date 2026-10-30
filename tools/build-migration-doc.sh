#!/usr/bin/env bash
# Собирает docs/MIGRATION.md из разделов docs/parts/*.md.
# Править нужно разделы, а не итоговый файл.
set -euo pipefail
cd "$(dirname "$0")/.."

out=docs/MIGRATION.md
{
  echo "# Zafiro → zafirogo: чек-лист переноса"
  echo
  echo "<!-- Файл собран tools/build-migration-doc.sh из docs/parts/*.md. Не редактировать вручную. -->"
  echo
  echo "Источник правды — старый проект https://github.com/aitmaganbetov/Zafiro (пути ниже — относительно его корня)."
  echo "Связанные документы: [бизнес-правила](business-rules.md), [эталонные сценарии](golden-scenarios.md),"
  echo "[изменения схемы](schema-changes.md), [протокол агента](agent-protocol.md), [открытые вопросы](OPEN_QUESTIONS.md)."
  echo
  echo "| Раздел | Пунктов |"
  echo "|---|---:|"
  for f in docs/parts/*.md; do
    title=$(grep -m1 '^# ' "$f" | sed 's/^# //')
    count=$(grep -c '^\s*- \[ \]' "$f" || true)
    echo "| ${title} | ${count} |"
  done
  echo
  for f in docs/parts/*.md; do
    echo
    # Заголовки разделов сдвигаются на уровень ниже.
    sed 's/^#/##/' "$f"
  done
} > "$out"
echo "собрано $out"
