using Google.Protobuf.WellKnownTypes;
using Zafiro.Agent.Core.Runtime;
using Zafiro.Agent.Ipc.V1;
using Zafiro.Agent.V1;

namespace Zafiro.Agent.Tests;

public class AgentStateTests
{
    private static GuestSignIn SignIn(long user, long main = 100_00) => new()
    {
        Token = new GuestToken { AccessToken = "secret-token" },
        User = new GuestUser { UserId = user, Name = "Гость" },
        Balance = new Balance { MainTiyn = main, Currency = "KZT" },
    };

    [Fact]
    public void Состояния_ПК_и_экран()
    {
        var s = new AgentState();
        s.SetConnection(Connection.Online);
        Assert.Equal(ComputerState.Idle, s.ComputerState);
        Assert.Equal(Screen.Lock, s.Screen);

        s.SignIn(SignIn(5));
        Assert.Equal(ComputerState.Locked, s.ComputerState);
        Assert.Equal(5, s.BuildStatus(new ComputerLoad()).SignedInUserId);

        s.StartSession(new ActiveSession { SessionId = "a", UserId = 5, EndsAt = Timestamp.FromDateTimeOffset(DateTimeOffset.UtcNow.AddHours(1)) });
        Assert.Equal(ComputerState.InSession, s.ComputerState);
        Assert.Equal(Screen.None, s.Screen);
        Assert.Equal("a", s.BuildStatus(new ComputerLoad()).SessionId);

        s.Lock("Подойдите к кассе");
        Assert.Equal(Screen.Lock, s.Screen);
        Assert.True(s.Unlock());
        Assert.Equal(Screen.None, s.Screen);

        Assert.False(s.EndSession("b"));
        Assert.True(s.EndSession("a"));
        Assert.True(s.EndSession("a")); // повтор EndSession — не ошибка
        Assert.Equal(ComputerState.Locked, s.ComputerState);
    }

    [Fact]
    public void Сессия_истекает_по_часам_сервера()
    {
        var s = new AgentState();
        s.ApplyWelcome(new Welcome { ServerTime = Timestamp.FromDateTimeOffset(DateTimeOffset.UtcNow.AddHours(2)) });
        s.StartSession(new ActiveSession { SessionId = "x", EndsAt = Timestamp.FromDateTimeOffset(DateTimeOffset.UtcNow.AddHours(1)) });
        // По часам ПК сессия идёт ещё час, по часам сервера — уже закончилась.
        Assert.NotEqual(ComputerState.InSession, s.ComputerState);
        Assert.Equal("x", s.ExpireSessionIfDue());
        Assert.Null(s.ExpireSessionIfDue());
    }

    [Fact]
    public void Чужой_баланс_и_чужой_выход_игнорируются()
    {
        var s = new AgentState();
        s.SignIn(SignIn(5, 1000));
        Assert.False(s.SetBalance(6, new Balance { MainTiyn = 1 }));
        Assert.Equal(1000, s.BuildSnapshot().Balance.MainTiyn);
        Assert.True(s.SetBalance(5, new Balance { MainTiyn = 700 }));
        Assert.Equal(700, s.BuildSnapshot().Balance.MainTiyn);
        Assert.Equal(0, s.SignOut(onlyUserId: 6));
        Assert.Equal(5, s.SignOut(onlyUserId: 5));
        Assert.Null(s.GuestToken);
    }

    [Fact]
    public void Снимок_для_UI_без_токена_гостя()
    {
        var s = new AgentState();
        s.SignIn(SignIn(5));
        var snap = s.BuildSnapshot();
        Assert.DoesNotContain("secret-token", snap.ToString(), StringComparison.Ordinal);
        Assert.Equal("secret-token", s.GuestToken);
        Assert.Null(Zafiro.Agent.Core.Guest.GuestApi.Redacted(SignIn(5)).Token);
    }
}
