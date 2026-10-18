import { useEffect, useState } from 'react';

// usePageVisible reports whether the document is visible, so polling can
// pause while the browser tab is hidden.
export function usePageVisible(): boolean {
  const [visible, setVisible] = useState(() => document.visibilityState !== 'hidden');
  useEffect(() => {
    const onChange = () => setVisible(document.visibilityState !== 'hidden');
    document.addEventListener('visibilitychange', onChange);
    return () => document.removeEventListener('visibilitychange', onChange);
  }, []);
  return visible;
}
