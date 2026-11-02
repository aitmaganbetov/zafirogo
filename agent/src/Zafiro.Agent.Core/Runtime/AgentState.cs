using Google.Protobuf.WellKnownTypes;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Core.Runtime;

/// <summary>
/// Состояние ПК в службе: связь, снимок Welcome, сессия, вошедший гость (токен — только здесь, в памяти службы),
/// техрежим, блокировка сотрудником. Из него строятся Status для сервера и Snapshot для UI. Потокобезопасно;
/// <see cref="Changed"/> вызывается после каждого изменения (вне блокировки).
/// </summary>
public sealed class AgentState
{
    private readonly object _gate = new();
    private readonly TimeProvider _time;
    private Connection _connection = Connection.NotEnrolled;
    private ComputerRef? _computer;
    private ClubConfig? _config;
    private ActiveSession? _session;
    private GuestSignIn? _guest;
    private TimeSpan _clockOffset;
    private DateTimeOffset? _serviceUntil;
    private bool _shuttingDown;
    private string? _lockReason;
    private bool _locked;
    private uint _idleSeconds;
    private long _translationsVersion;
    private long _serverTranslationsVersion;
    private string _clubName = "";
    private TimeSpan _statusInterval = TimeSpan.FromSeconds(60);
    private DateTimeOffset? _renewAfterOverride;

    public AgentState(TimeProvider? time = null) => _time = time ?? TimeProvider.System;

    /// <summary>Любое изменение состояния (для Status и событий UI).</summary>
    public event Action? Changed;

    /// <summary>Время по часам сервера (Welcome.server_time, Ping): конец сессии считается по нему.</summary>
    public DateTimeOffset ServerNow()
    {
        lock (_gate) return _time.GetUtcNow() + _clockOffset;
    }

    public Connection Connection
    {
        get
        {
            lock (_gate) return _connection;
        }
    }

    public TimeSpan StatusInterval
    {
        get
        {
            lock (_gate) return _statusInterval;
        }
    }

    public ClubConfig? Config
    {
        get
        {
            lock (_gate) return _config?.Clone();
        }
    }

    public ActiveSession? Session
    {
        get
        {
            lock (_gate) return _session?.Clone();
        }
    }

    /// <summary>Токен вошедшего гостя (null — никто не вошёл). Наружу (UI, журналы) не передаётся.</summary>
    public string? GuestToken
    {
        get
        {
            lock (_gate) return _guest?.Token?.AccessToken is { Length: > 0 } t ? t : null;
        }
    }

    public long SignedInUserId
    {
        get
        {
            lock (_gate) return _guest?.User?.UserId ?? 0;
        }
    }

    public long TranslationsVersion
    {
        get
        {
            lock (_gate) return _translationsVersion;
        }
    }

    public long ServerTranslationsVersion
    {
        get
        {
            lock (_gate) return _serverTranslationsVersion;
        }
    }

    public DateTimeOffset? ServiceUntil
    {
        get
        {
            lock (_gate) return _serviceUntil;
        }
    }

    public DateTimeOffset? CertificateRenewAfterOverride
    {
        get
        {
            lock (_gate) return _renewAfterOverride;
        }
    }

    public uint IdleSeconds
    {
        get
        {
            lock (_gate) return _idleSeconds;
        }
    }

    /// <summary>Текущее состояние ПК (ComputerState) — выводится из остального.</summary>
    public ComputerState ComputerState
    {
        get
        {
            lock (_gate) return ComputeStateLocked();
        }
    }

    private ComputerState ComputeStateLocked()
    {
        if (_shuttingDown) return ComputerState.ShuttingDown;
        if (_serviceUntil is { } until && until > _time.GetUtcNow()) return ComputerState.Service;
        if (HasSessionLocked()) return ComputerState.InSession;
        if (_guest is not null) return ComputerState.Locked;
        return ComputerState.Idle;
    }

    private bool HasSessionLocked() =>
        _session is not null && (_session.EndsAt is null || _session.EndsAt.ToDateTimeOffset() > _time.GetUtcNow() + _clockOffset);

    /// <summary>Какой экран нужен UI.</summary>
    public Screen Screen
    {
        get
        {
            lock (_gate) return ScreenLocked();
        }
    }

    private Screen ScreenLocked()
    {
        if (_connection is Connection.NotEnrolled or Connection.Revoked or Connection.Enrolling or Connection.PendingApproval) return Screen.Setup;
        return ComputeStateLocked() switch
        {
            ComputerState.Service or ComputerState.ShuttingDown => Screen.None,
            ComputerState.InSession => _locked ? Screen.Lock : Screen.None,
            _ => Screen.Lock,
        };
    }

    public void SetConnection(Connection c) => Mutate(() =>
    {
        if (_connection == c) return false;
        _connection = c;
        return true;
    });

    public void SetLocalTranslationsVersion(long v) => Mutate(() =>
    {
        _translationsVersion = v;
        return true;
    });

    /// <summary>Welcome — полный снимок: конфигурация, ПК, сессия, часы сервера.</summary>
    public void ApplyWelcome(Welcome w)
    {
        ArgumentNullException.ThrowIfNull(w);
        Mutate(() =>
        {
            _connection = Connection.Online;
            _computer = w.Computer?.Clone();
            _config = w.Config?.Clone();
            _clubName = w.Config?.ClubName ?? _clubName;
            if (w.ServerTime is not null) _clockOffset = w.ServerTime.ToDateTimeOffset() - _time.GetUtcNow();
            if (w.StatusInterval is not null && w.StatusInterval.ToTimeSpan() >= TimeSpan.FromSeconds(5)) _statusInterval = w.StatusInterval.ToTimeSpan();
            _session = w.Session?.Clone();
            _serverTranslationsVersion = w.TranslationsVersion;
            _renewAfterOverride = w.CertificateRenewAfter?.ToDateTimeOffset();
            return true;
        });
    }

    /// <summary>Ping / server_time — подстройка часов.</summary>
    public void SyncClock(Timestamp serverTime)
    {
        ArgumentNullException.ThrowIfNull(serverTime);
        lock (_gate) _clockOffset = serverTime.ToDateTimeOffset() - _time.GetUtcNow();
    }

    public void ApplyConfig(ClubConfig config) => Mutate(() =>
    {
        _config = config.Clone();
        if (!string.IsNullOrEmpty(config.ClubName)) _clubName = config.ClubName;
        return true;
    });

    public void SetServerTranslationsVersion(long v) => Mutate(() =>
    {
        _serverTranslationsVersion = v;
        return true;
    });

    /// <summary>StartSession / покупка: новая или продлённая сессия.</summary>
    public void StartSession(ActiveSession s) => Mutate(() =>
    {
        _session = s.Clone();
        _locked = false;
        _lockReason = null;
        return true;
    });

    /// <summary>EndSession: сессия <paramref name="sessionId"/> завершена (или её уже нет). false — на ПК другая сессия (SESSION_MISMATCH).</summary>
    public bool EndSession(string sessionId)
    {
        var ok = false;
        Mutate(() =>
        {
            if (_session is null)
            {
                // Уже завершена (истекла по часам сервера или гость завершил сам) — повтор безопасен.
                ok = true;
                return false;
            }

            if (!string.IsNullOrEmpty(sessionId) && _session.SessionId != sessionId) return false;
            _session = null;
            ok = true;
            return true;
        });
        return ok;
    }

    /// <summary>Сессия истекла по часам сервера (без команды): вернуть её id и очистить.</summary>
    public string? ExpireSessionIfDue()
    {
        string? ended = null;
        Mutate(() =>
        {
            if (_session?.EndsAt is null || _session.EndsAt.ToDateTimeOffset() > _time.GetUtcNow() + _clockOffset) return false;
            ended = _session.SessionId;
            _session = null;
            return true;
        });
        return ended;
    }

    /// <summary>Вход гостя (Login / Register / QR).</summary>
    public void SignIn(GuestSignIn signIn)
    {
        ArgumentNullException.ThrowIfNull(signIn);
        Mutate(() =>
        {
            _guest = signIn.Clone();
            if (signIn.Session is not null) _session = signIn.Session.Clone();
            _idleSeconds = 0;
            return true;
        });
    }

    /// <summary>Выход гостя. Возвращает user_id вышедшего (0 — никто не был в системе).</summary>
    public long SignOut(long onlyUserId = 0)
    {
        long user = 0;
        Mutate(() =>
        {
            if (_guest is null) return false;
            if (onlyUserId > 0 && _guest.User?.UserId != onlyUserId) return false;
            user = _guest.User?.UserId ?? 0;
            _guest = null;
            return true;
        });
        return user;
    }

    /// <summary>BalanceChanged / ответ покупки. Чужой баланс игнорируется.</summary>
    public bool SetBalance(long userId, Balance balance)
    {
        var applied = false;
        Mutate(() =>
        {
            if (_guest is null || (userId > 0 && _guest.User?.UserId != userId)) return false;
            _guest.Balance = balance.Clone();
            applied = true;
            return true;
        });
        return applied;
    }

    public void EnterService(DateTimeOffset until) => Mutate(() =>
    {
        _serviceUntil = until;
        return true;
    });

    /// <summary>Техрежим закончился по времени: true — был активен.</summary>
    public bool ExitServiceIfDue()
    {
        var ended = false;
        Mutate(() =>
        {
            if (_serviceUntil is not { } until || until > _time.GetUtcNow()) return false;
            _serviceUntil = null;
            ended = true;
            return true;
        });
        return ended;
    }

    public void SetShuttingDown(bool v) => Mutate(() =>
    {
        if (_shuttingDown == v) return false;
        _shuttingDown = v;
        return true;
    });

    /// <summary>Lock: экран блокировки (с текстом).</summary>
    public void Lock(string? reason) => Mutate(() =>
    {
        _locked = true;
        _lockReason = string.IsNullOrEmpty(reason) ? null : reason;
        return true;
    });

    /// <summary>Unlock: false — снимать нечего (нет сессии и нет блокировки — COMMAND_REJECTED).</summary>
    public bool Unlock()
    {
        var ok = false;
        Mutate(() =>
        {
            if (!_locked && !HasSessionLocked()) return false;
            _locked = false;
            _lockReason = null;
            ok = true;
            return true;
        });
        return ok;
    }

    public string? LockReason
    {
        get
        {
            lock (_gate) return _lockReason;
        }
    }

    public void SetIdle(uint seconds)
    {
        lock (_gate) _idleSeconds = seconds;
    }

    /// <summary>Status для сервера.</summary>
    public Status BuildStatus(ComputerLoad load)
    {
        lock (_gate)
        {
            var st = ComputeStateLocked();
            var s = new Status
            {
                State = st,
                SignedInUserId = _guest?.User?.UserId ?? 0,
                Load = load,
                ObservedAt = Timestamp.FromDateTimeOffset(_time.GetUtcNow()),
            };
            if (st == ComputerState.InSession && _session is not null) s.SessionId = _session.SessionId;
            s.Load.IdleSeconds = _idleSeconds;
            return s;
        }
    }

    /// <summary>Снимок для UI (без токена гостя).</summary>
    public Snapshot BuildSnapshot()
    {
        lock (_gate)
        {
            var snap = new Snapshot
            {
                Connection = _connection,
                State = ComputeStateLocked(),
                Screen = ScreenLocked(),
                Computer = _computer?.Clone(),
                Config = _config?.Clone(),
                Session = HasSessionLocked() ? _session?.Clone() : null,
                Guest = _guest?.User?.Clone(),
                Balance = _guest?.Balance?.Clone(),
                ServerTime = Timestamp.FromDateTimeOffset(_time.GetUtcNow() + _clockOffset),
                TranslationsVersion = Math.Max(_translationsVersion, _serverTranslationsVersion),
                ClubName = _clubName,
            };
            if (_serviceUntil is { } until) snap.ServiceUntil = Timestamp.FromDateTimeOffset(until);
            return snap;
        }
    }

    private void Mutate(Func<bool> change)
    {
        bool changed;
        lock (_gate) changed = change();
        if (changed) Changed?.Invoke();
    }
}
