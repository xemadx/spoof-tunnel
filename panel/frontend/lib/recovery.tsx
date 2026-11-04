import type { RecoveryState } from './api';

export function RecoveryNotice({ state }: { state?: RecoveryState }) {
 if (!state?.active && !state?.exhausted) return null;
 return <div className={state.exhausted ? 'banner error' : 'banner warn'} role="status">
  {state.exhausted
   ? `بازیابی پس از ${state.max_attempts.toLocaleString('fa-IR')} تلاش متوقف شد. لاگ و تنظیمات را بررسی کن و دوباره راه‌اندازی کن.`
   : `بازیابی خودکار فعال است؛ ${state.attempts.toLocaleString('fa-IR')} از ${state.max_attempts.toLocaleString('fa-IR')} تلاش انجام شده.${state.next_retry_at ? ' تلاش بعدی: ' + new Date(state.next_retry_at * 1000).toLocaleTimeString('fa-IR') : ''}`}
 </div>;
}
