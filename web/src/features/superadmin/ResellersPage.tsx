import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useRef, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { idempotencyKey, isApiError } from '../../api/http';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { PARSE_MONEY_MESSAGES, formatMoney, parseMoney } from '../../lib/money';
import { trimHundredths } from '../reseller/percent';
import { firstError } from '../../api/errors';
import {
  createReseller, deleteReseller, detachResellerClub, payoutReseller, qs, resellersQuery, updateReseller,
  type ResellerCard, type ResellersSearch,
} from './api';
import { useSuperadminFlash } from './flash';
import { saHref } from './host';
import { SecondFactorDialog } from './SecondFactorDialog';

/*
 * Реселлеры — перенос resources/views/superadmin/resellers/index.blade.php (Superadmin\ResellerController):
 * суперадмин создаёт реселлеров (новый аккаунт или существующий по телефону), задаёт процент и скидку клубам,
 * записывает выплаты, отвязывает клубы и удаляет реселлеров.
 * Отличие от Blade (docs/open-questions/upstream-resellers.md UR-2): выплата и удаление необратимы, поэтому
 * подтверждаются окном со свежим кодом TOTP (как выдача прав суперадмина на экране «Пользователи»), а не сразу и
 * не window.confirm. Суммы и проценты уходят на сервер целыми (тиыны, базисные пункты) — без float.
 * Ключ идемпотентности выплаты живёт до успеха: ответ мог потеряться (обрыв, таймаут, 5xx), а выплата — записаться;
 * повтор с тем же ключом сервер узнаёт (reseller.Payout: Replayed) и второй выплаты не создаёт.
 */

const CLUBS_KEY = '{{count}} клуб|{{count}} клуба|{{count}} клубов';
const FIELD = 'rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100';

/** $money: number_format((float) $value, 0, '.', ' ').' ₸'. */
const money = (tiyn: number) => formatMoney(tiyn, 0, '.', ' ') + ' ₸';

/** Необратимое действие, которое ждёт кода: выплата (с ключом идемпотентности формы) или удаление. */
/** Ключ незавершённой выплаты реселлера и данные, с которыми он ушёл (сервер сверяет сумму и комментарий). */
interface PayoutKey {
  readonly key: string;
  readonly amount: number;
  readonly note: string;
  /** «Выплачено» на момент создания ключа: изменилось — та выплата уже записана, следующая отправка формы — новая. */
  readonly paidOut: number;
}

/** Ответа нет или сервер упал: исход запроса неизвестен — действие могло выполниться. */
const outcomeUnknown = (e: unknown) => !isApiError(e) || e.kind === 'network' || e.status >= 500;

type Pending =
  | { readonly kind: 'payout'; readonly reseller: ResellerCard; readonly amount: number; readonly note: string; readonly key: string; readonly form: HTMLFormElement }
  | { readonly kind: 'delete'; readonly reseller: ResellerCard };

export function ResellersPage({ search }: { readonly search: ResellersSearch }) {
  const { t } = useTranslation();
  const flash = useSuperadminFlash();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data } = useQuery(resellersQuery(search));
  const [pending, setPending] = useState<Pending | null>(null);
  const [codeError, setCodeError] = useState<string | null>(null);
  useAdminPageTitle(t('Реселлеры'));

  const done = (r: { message: string }) => {
    flash.status(r.message);
    return queryClient.invalidateQueries({ queryKey: ['superadmin'] });
  };
  const fail = (e: unknown) => flash.error(firstError(e));
  const payoutKeys = useRef(new Map<number, PayoutKey>());
  const create = useMutation({ mutationFn: createReseller, onError: fail });
  const update = useMutation({ mutationFn: ({ id, body }: { id: number; body: Parameters<typeof updateReseller>[1] }) => updateReseller(id, body), onSuccess: done, onError: fail });
  const detach = useMutation({ mutationFn: ({ id, club }: { id: number; club: number }) => detachResellerClub(id, club), onSuccess: done, onError: fail });
  const confirmed = useMutation({
    mutationFn: ({ action, code }: { action: Pending; code: string }) =>
      action.kind === 'payout'
        ? payoutReseller(action.reseller.id, { amount_tiyn: action.amount, note: action.note === '' ? null : action.note, code }, action.key)
        : deleteReseller(action.reseller.id, code),
    onSuccess: (r, { action }) => {
      setPending(null);
      if (action.kind === 'payout') {
        // Ключ отработал — следующая выплата этому реселлеру пойдёт с новым.
        payoutKeys.current.delete(action.reseller.id);
        action.form.reset();
      }
      return done(r);
    },
    onError: (e, { action }) => {
      // Список перечитывается при любом исходе: выплата или удаление могли пройти, хотя ответ не дошёл.
      void queryClient.invalidateQueries({ queryKey: ['superadmin'] });
      // Ошибка кода (неверный, уже использованный, не введён) — под полем, окно остаётся открытым.
      const code = isApiError(e) ? e.field('code') : undefined;
      if (code) {
        setCodeError(code);
        return;
      }
      // Исход выплаты неизвестен — окно остаётся: повтор уйдёт с тем же ключом и второй выплаты не создаст.
      if (action.kind === 'payout' && outcomeUnknown(e)) {
        setCodeError(firstError(e) || t('Нет связи с сервером. Повторите попытку.'));
        return;
      }
      setPending(null);
      fail(e);
    },
  });

  if (!data) return null;

  const text = (f: FormData, name: string) => String(f.get(name) ?? '').trim();
  /** Процент из поля формы → базисные пункты (не больше двух знаков после точки). */
  const percent = (raw: string): number | null => {
    const p = parseMoney(raw);
    if (!p.ok) {
      flash.error(t(PARSE_MONEY_MESSAGES[p.error], { attribute: 'commission percent' }));
      return null;
    }
    return p.tiyn;
  };

  const submitCreate = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    const f = new FormData(form);
    const bp = percent(text(f, 'commission_percent'));
    if (bp === null) return;
    const password = String(f.get('password') ?? '');
    create.mutate(
      { name: text(f, 'name'), phone: text(f, 'phone'), password: password === '' ? null : password, code: text(f, 'code'), commission_bp: bp, discount_percent: Number(text(f, 'discount_percent')) },
      { onSuccess: (r) => { form.reset(); return done(r); } },
    );
  };
  const submitSearch = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    void navigate({ href: saHref('/resellers' + qs({ q: text(new FormData(e.currentTarget), 'q') })) });
  };
  const submitUpdate = (reseller: ResellerCard) => (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const bp = percent(text(f, 'commission_percent'));
    if (bp === null) return;
    update.mutate({ id: reseller.id, body: { commission_bp: bp, discount_percent: Number(text(f, 'discount_percent')), is_active: f.get('is_active') === '1' } });
  };
  const submitPayout = (reseller: ResellerCard) => (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    const f = new FormData(form);
    const amount = parseMoney(text(f, 'amount'));
    if (!amount.ok) {
      flash.error(t(PARSE_MONEY_MESSAGES[amount.error], { attribute: 'amount' }));
      return;
    }
    setCodeError(null);
    const note = text(f, 'note');
    // Ключ — на выплату, а не на отправку формы: он живёт до успеха, пока не изменились реселлер, сумма и комментарий
    // (сервер на тот же ключ с другими данными ответит конфликтом). Закрытое после ошибки окно ключ не сбрасывает.
    const kept = payoutKeys.current.get(reseller.id);
    const same = kept && kept.amount === amount.tiyn && kept.note === note && kept.paidOut === reseller.paid_out_tiyn;
    const key = same ? kept.key : idempotencyKey();
    payoutKeys.current.set(reseller.id, { key, amount: amount.tiyn, note, paidOut: reseller.paid_out_tiyn });
    setPending({ kind: 'payout', reseller, amount: amount.tiyn, note, key, form });
  };
  const submitDelete = (reseller: ResellerCard) => (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setCodeError(null);
    setPending({ kind: 'delete', reseller });
  };
  const submitDetach = (reseller: ResellerCard, club: number) => (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (window.confirm(t('Отвязать клуб от реселлера? Новые счета пойдут без его скидки и процента.'))) detach.mutate({ id: reseller.id, club });
  };
  const question =
    pending === null
      ? null
      : pending.kind === 'payout'
        ? t('Выплата реселлеру {{code}}: {{amount}} ₸', { code: pending.reseller.code, amount: formatMoney(pending.amount, 2, '.', ' ') })
        : t('Удалить реселлера {{code}}? Аккаунт останется, клубы отвяжутся, выплаты удалятся.', { code: pending.reseller.code });

  return (
    <>
      <p className="mb-4 text-sm text-gray-600 dark:text-gray-400">{t('Реселлеров создаёте вы: они входят в свой кабинет на {{host}} по телефону и паролю. Клуб, который ввёл код реселлера, платит подписку с его скидкой, а реселлер получает свой процент с каждой оплаты.', { host: data.host })}</p>

      <form method="POST" className="mb-6 rounded-xl border border-dashed border-violet-300 bg-white p-4 dark:border-violet-800 dark:bg-gray-900" onSubmit={submitCreate}>
        <h3 className="mb-3 font-semibold text-gray-900 dark:text-white">{t('Новый реселлер')}</h3>
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          <label className="text-sm"><span className="mb-1 block text-gray-600 dark:text-gray-400">{t('Имя или компания')}</span><input type="text" name="name" required maxLength={120} className={`${FIELD} w-full`} /></label>
          <label className="text-sm"><span className="mb-1 block text-gray-600 dark:text-gray-400">{t('Телефон')}</span><input type="tel" name="phone" required placeholder="+7 700 123-45-67" className={`${FIELD} w-full`} /></label>
          <label className="text-sm"><span className="mb-1 block text-gray-600 dark:text-gray-400">{t('Пароль')}</span><input type="text" name="password" minLength={6} autoComplete="new-password" className={`${FIELD} w-full`} /><span className="mt-1 block text-xs text-gray-500">{t('Если аккаунт с этим телефоном уже есть, оставьте пустым: пароль останется прежним.')}</span></label>
          <label className="text-sm"><span className="mb-1 block text-gray-600 dark:text-gray-400">{t('Код реселлера')}</span><input type="text" name="code" required maxLength={32} autoCapitalize="none" spellCheck={false} placeholder="arman-games" className={`${FIELD} w-full`} /><span className="mt-1 block text-xs text-gray-500">{t('Латинские буквы, цифры и дефис, от 3 до 32 символов.')}</span></label>
          <label className="text-sm"><span className="mb-1 block text-gray-600 dark:text-gray-400">{t('Процент реселлера')}</span><input type="number" name="commission_percent" min="0" max="100" step="0.01" defaultValue="0" required className={`${FIELD} w-full`} /></label>
          <label className="text-sm"><span className="mb-1 block text-gray-600 dark:text-gray-400">{t('Скидка клубам, %')}</span><input type="number" name="discount_percent" min="0" max="100" step="1" defaultValue="0" required className={`${FIELD} w-full`} /></label>
        </div>
        <button type="submit" disabled={create.isPending} className="brand-button mt-3 rounded-lg px-4 py-2 text-sm">{t('Создать реселлера')}</button>
      </form>

      <form method="GET" className="mb-4 flex flex-wrap gap-2" onSubmit={submitSearch}>
        <input type="search" name="q" defaultValue={search.q ?? ''} placeholder={t('Код или имя')} className={FIELD} />
        <button type="submit" className="rounded-lg border border-gray-300 px-4 py-2 text-sm">{t('Найти')}</button>
      </form>

      <div className="space-y-4">
        {data.items.map((reseller) => (
          <article key={reseller.id} className="rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-900">
            <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
              <div>
                <h3 className="font-semibold text-gray-900 dark:text-white"><span className="font-mono">{reseller.code}</span> · {reseller.name}</h3>
                <p className="text-sm text-gray-500">{reseller.user_name} · {reseller.user_phone} · {t(CLUBS_KEY, { count: reseller.clubs.length })}</p>
              </div>
              <div className="flex flex-wrap items-baseline gap-3">
                <p className="text-sm text-gray-700 dark:text-gray-300">{t('Заработано {{earned}}, выплачено {{paid}}, к выплате {{balance}}', { earned: money(reseller.earned_tiyn), paid: money(reseller.paid_out_tiyn), balance: money(reseller.balance_tiyn) })}</p>
                <form method="POST" onSubmit={submitDelete(reseller)}><button type="submit" className="text-sm text-red-600 hover:underline">{t('Удалить')}</button></form>
              </div>
            </div>
            <div className="grid gap-4 lg:grid-cols-2">
              {/* key: после сохранения поля берут новые значения (как перезагрузка страницы). */}
              <form key={`${reseller.commission_bp}|${reseller.discount_percent}|${reseller.is_active}`} method="POST" className="flex flex-wrap items-end gap-3" onSubmit={submitUpdate(reseller)}>
                <label className="text-sm"><span className="mb-1 block text-gray-600 dark:text-gray-400">{t('Процент реселлера')}</span><input type="number" name="commission_percent" min="0" max="100" step="0.01" defaultValue={trimHundredths(reseller.commission_bp)} required className={`${FIELD} w-28`} /></label>
                <label className="text-sm"><span className="mb-1 block text-gray-600 dark:text-gray-400">{t('Скидка клубам, %')}</span><input type="number" name="discount_percent" min="0" max="100" step="1" defaultValue={reseller.discount_percent} required className={`${FIELD} w-28`} /></label>
                <label className="flex items-center gap-2 pb-2 text-sm text-gray-700 dark:text-gray-300"><input type="checkbox" name="is_active" value="1" defaultChecked={reseller.is_active} /> {t('Код действует')}</label>
                <button type="submit" disabled={update.isPending} className="rounded-lg border border-gray-300 px-4 py-2 text-sm">{t('Сохранить')}</button>
              </form>
              <form key={reseller.balance_tiyn} method="POST" className="flex flex-wrap items-end gap-3" onSubmit={submitPayout(reseller)}>
                <label className="text-sm"><span className="mb-1 block text-gray-600 dark:text-gray-400">{t('Выплата, ₸')}</span><input type="number" name="amount" min="0.01" step="0.01" defaultValue={reseller.balance_tiyn > 0 ? trimHundredths(reseller.balance_tiyn) : ''} required className={`${FIELD} w-32`} /></label>
                <label className="min-w-0 flex-1 text-sm"><span className="mb-1 block text-gray-600 dark:text-gray-400">{t('Комментарий')}</span><input type="text" name="note" maxLength={255} placeholder={t('Например: Kaspi, ноябрь')} className={`${FIELD} w-full`} /></label>
                <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">{t('Записать выплату')}</button>
              </form>
            </div>
            {reseller.clubs.length > 0 && (
              <details className="mt-3 text-sm">
                <summary className="cursor-pointer text-gray-600 dark:text-gray-400">{t('Клубы реселлера')}</summary>
                <ul className="mt-2 divide-y divide-gray-100 dark:divide-gray-800">
                  {reseller.clubs.map((club) => (
                    <li key={club.id} className="flex items-center justify-between gap-3 py-2">
                      <a href={saHref(`/clubs/${club.id}`)} className="text-gray-900 hover:underline dark:text-white">{club.name}</a>
                      <span className="text-gray-500">{club.attached}</span>
                      <form method="POST" onSubmit={submitDetach(reseller, club.id)}><button type="submit" disabled={detach.isPending} className="text-red-600 hover:underline">{t('Отвязать')}</button></form>
                    </li>
                  ))}
                </ul>
              </details>
            )}
          </article>
        ))}
        {data.items.length === 0 && <p className="text-sm text-gray-500">{t('Реселлеров пока нет.')}</p>}
      </div>
      <div className="mt-4">
        <Pagination page={data.page} perPage={data.per_page} total={data.total} count={data.items.length} hrefFor={(p) => saHref('/resellers' + qs({ q: search.q, page: p > 1 ? p : undefined }))} />
      </div>

      <SecondFactorDialog
        question={question}
        error={codeError}
        busy={confirmed.isPending}
        onClose={() => setPending(null)}
        onSubmit={(code) => {
          if (!pending) return;
          setCodeError(null);
          confirmed.mutate({ action: pending, code });
        }}
      />
    </>
  );
}
