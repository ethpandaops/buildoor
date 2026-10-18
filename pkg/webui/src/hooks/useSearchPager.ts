import { useEffect, useMemo, useState } from 'react';

export interface SearchPager<T> {
  search: string;
  setSearch: (term: string) => void;
  // Rows that pass the search and the extra filter.
  filtered: T[];
  // The current page of filtered.
  page: T[];
  offset: number;
  limit: number;
  setOffset: (offset: number) => void;
}

// useSearchPager filters a client-side list by a case-insensitive search term
// over the given fields (plus an optional extra predicate) and pages the
// result. The offset resets on a new search or filter and is clamped when the
// list shrinks under it.
export function useSearchPager<T>(
  items: T[],
  fields: (item: T) => Array<string | number | null | undefined>,
  options: { limit?: number; predicate?: (item: T) => boolean; filterKey?: string } = {},
): SearchPager<T> {
  const { limit = 50, predicate, filterKey = '' } = options;
  const [search, setSearch] = useState('');
  const [offset, setOffset] = useState(0);

  const filtered = useMemo(() => {
    const term = search.trim().toLowerCase();
    return items.filter((item) => {
      if (predicate && !predicate(item)) return false;
      if (!term) return true;
      return fields(item).some((v) => v !== null && v !== undefined && String(v).toLowerCase().includes(term));
    });
    // fields/predicate are inline closures: filterKey stands in for the
    // state the predicate depends on.
  }, [items, search, filterKey]);

  useEffect(() => {
    setOffset(0);
  }, [search, filterKey]);

  const total = filtered.length;
  useEffect(() => {
    const maxOffset = total === 0 ? 0 : Math.floor((total - 1) / limit) * limit;
    if (offset > maxOffset) setOffset(maxOffset);
  }, [total, offset, limit]);

  const page = useMemo(() => filtered.slice(offset, offset + limit), [filtered, offset, limit]);

  return { search, setSearch, filtered, page, offset, limit, setOffset };
}
