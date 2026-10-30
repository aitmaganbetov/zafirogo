import { describe, expect, it } from 'vitest';
import { canvasHeight, collides, computerClasses, formatPreviewSum, guestPreview, snapComputer, snapSize, snapZone } from './logic';

describe('club map logic', () => {
  it('canvas height: not lower than 430', () => {
    expect(canvasHeight([])).toBe(430);
    expect(canvasHeight([{ x: 0, y: 300, width: 100, height: 200 }])).toBe(530);
  });

  it('collides with a 20px gap', () => {
    const other = [{ x: 20, y: 20, width: 300, height: 180 }];
    expect(collides(other, 330, 20, 100, 100)).toBe(true); // 10 px от правого края
    expect(collides(other, 340, 20, 100, 100)).toBe(false); // ровно 20 px
  });

  it('snaps zones and computers like the template', () => {
    expect(snapZone(20, 37)).toBe(60);
    expect(snapZone(20, -100)).toBe(0);
    expect(snapSize(320, -500, 180)).toBe(180);
    expect(snapComputer(5, 30, 47, 300)).toBe(52);
    expect(snapComputer(5, 1000, 47, 300)).toBe(287);
    expect(snapComputer(5, -100, 47, 300)).toBe(5);
  });

  it('computer classes', () => {
    expect(computerClasses('online', 'pc-free', true)).toBe('cm-computer online pc-free selected');
    expect(computerClasses('offline', '', false)).toBe('cm-computer offline');
  });

  // Q-019: предпросмотр — те же тиыны, что pricing.MinutesPrice на сервере: intdiv(час·мин + 30, 60).
  it('guest preview in tiyn', () => {
    expect(guestPreview({ kind: 'minutes', minutes: 60 }, [60000, 100000])).toEqual({ missing: false, sumTiyn: 160000 });
    expect(guestPreview({ kind: 'minutes', minutes: 7 }, [100001])).toEqual({ missing: false, sumTiyn: 11667 }); // (700007 + 30) / 60
    expect(guestPreview({ kind: 'minutes', minutes: 1 }, [null, 60000]).missing).toBe(true);
    expect(guestPreview({ kind: 'tariff', priceTiyn: 50000 }, [1, 2, 3])).toEqual({ missing: false, sumTiyn: 150000 });
  });

  it('formats the preview sum without float', () => {
    expect(formatPreviewSum(160000, 'en')).toBe('1,600');
    expect(formatPreviewSum(11667, 'en')).toBe('116.67');
    expect(formatPreviewSum(11650, 'en')).toBe('116.5');
  });
});
