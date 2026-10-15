// Display helpers shared by the mempool page tabs.

export function weiToEth(wei: string | undefined): string {
  if (!wei) return '0';
  try {
    const value = BigInt(wei);
    const whole = value / 1000000000000000000n;
    const frac = (value % 1000000000000000000n).toString().padStart(18, '0').slice(0, 6);
    return `${whole}.${frac}`;
  } catch {
    return wei;
  }
}

export function weiToGwei(wei: string | undefined): string {
  if (!wei) return '0';
  try {
    const value = Number(BigInt(wei)) / 1e9;
    return value >= 100 ? value.toFixed(0) : value.toFixed(3);
  } catch {
    return wei;
  }
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / 1024 / 1024).toFixed(2)} MiB`;
}

export function relativeTime(iso: string | undefined): string {
  if (!iso) return '—';
  const diff = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(diff)) return '—';
  if (diff < 1000) return 'just now';
  if (diff < 60000) return `${Math.floor(diff / 1000)}s ago`;
  if (diff < 3600000) return `${Math.floor(diff / 60000)}m ago`;
  if (diff < 86400000) return `${Math.floor(diff / 3600000)}h ago`;
  return `${Math.floor(diff / 86400000)}d ago`;
}

export function formatRate(rate: number | undefined): string {
  if (rate === undefined) return '—';
  return rate === 0 ? 'unlimited' : `${rate}/s`;
}
