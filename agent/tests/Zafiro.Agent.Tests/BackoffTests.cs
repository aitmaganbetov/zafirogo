using Zafiro.Agent.Core.Transport;

namespace Zafiro.Agent.Tests;

public class BackoffTests
{
    [Theory]
    [InlineData(0, 1)]
    [InlineData(1, 2)]
    [InlineData(2, 4)]
    [InlineData(5, 32)]
    [InlineData(6, 60)]
    [InlineData(30, 60)]
    [InlineData(-3, 1)]
    public void Потолок_min_60с_и_2_в_степени_attempt(int attempt, int seconds) =>
        Assert.Equal(TimeSpan.FromSeconds(seconds), Backoff.Ceiling(attempt));

    [Fact]
    public void Full_jitter_в_пределах_окна_и_покрывает_его()
    {
        var random = new Random(42);
        var samples = Enumerable.Range(0, 2000).Select(_ => Backoff.Next(3, random)).ToList();
        Assert.All(samples, d => Assert.InRange(d, TimeSpan.Zero, TimeSpan.FromSeconds(8)));
        // Равномерно: есть и короткие, и длинные задержки (full jitter, а не «2^n ± немного»).
        Assert.Contains(samples, d => d < TimeSpan.FromSeconds(1));
        Assert.Contains(samples, d => d > TimeSpan.FromSeconds(7));
    }

    [Fact]
    public void RetryInfo_сервера_нижняя_граница()
    {
        var random = new Random(1);
        for (var i = 0; i < 100; i++)
            Assert.True(Backoff.Next(0, random, TimeSpan.FromSeconds(30)) >= TimeSpan.FromSeconds(30));
    }

    [Fact]
    public void Drain_случайно_в_окне_по_умолчанию_30с()
    {
        var random = new Random(7);
        for (var i = 0; i < 500; i++)
        {
            Assert.InRange(Backoff.Drain(TimeSpan.FromSeconds(10), random), TimeSpan.Zero, TimeSpan.FromSeconds(10));
            Assert.InRange(Backoff.Drain(null, random), TimeSpan.Zero, TimeSpan.FromSeconds(30));
        }
    }

    [Fact]
    public void Продление_после_renew_after_с_jitter_до_суток_но_до_истечения()
    {
        var random = new Random(3);
        var renewAfter = new DateTimeOffset(2026, 11, 20, 0, 0, 0, TimeSpan.Zero);
        var notAfter = renewAfter.AddDays(10);
        for (var i = 0; i < 200; i++) Assert.InRange(Backoff.RenewAt(renewAfter, notAfter, random), renewAfter, renewAfter.AddHours(24));

        // Бездисковый сертификат (24 ч, renew_after через 16 ч): окно ≤ 90 % оставшихся 8 ч.
        var shortNotAfter = renewAfter.AddHours(8);
        for (var i = 0; i < 200; i++) Assert.InRange(Backoff.RenewAt(renewAfter, shortNotAfter, random), renewAfter, renewAfter.AddHours(7.2));
    }
}
