// Display helpers shared by the dashboard cards.

export function gweiToEthText(gwei: number, digits = 4): string {
  const eth = gwei / 1e9;
  return eth.toLocaleString('en-US', { maximumFractionDigits: digits });
}

export function weiToEthText(wei: string | undefined, digits = 2): string {
  if (!wei) return '—';
  try {
    const eth = Number(BigInt(wei) / 10n ** 9n) / 1e9;
    return eth.toLocaleString('en-US', { maximumFractionDigits: digits });
  } catch {
    return '—';
  }
}

export function count(n: number | undefined | null): string {
  return (n ?? 0).toLocaleString('en-US');
}
