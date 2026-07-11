import { useState, type FormEvent } from 'react';

/**
 * Shown when the API answers 401 (OWLWATCH_TOKEN is set): a centered card
 * with one password input. The submitted token is stored by the caller
 * (localStorage `owlwatch-token`) and attached to every request.
 */
export function TokenGate({
  failed,
  onSubmit,
}: {
  /** True when a previously submitted token was rejected. */
  failed: boolean;
  onSubmit: (token: string) => void;
}) {
  const [value, setValue] = useState('');

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const token = value.trim();
    if (token) onSubmit(token);
  };

  return (
    <div className="gate">
      <form className="card gate-card" onSubmit={submit}>
        <div className="gate-mark" aria-hidden="true">
          🦉
        </div>
        <h1 className="gate-title">owlwatch</h1>
        <p className="gate-hint">This dashboard requires an access token.</p>
        <input
          type="password"
          className="gate-input"
          placeholder="Access token"
          aria-label="Access token"
          autoFocus
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
        <button type="submit" className="gate-btn">
          Save
        </button>
        {failed && (
          <p className="gate-error" role="alert">
            ▲ That token wasn’t accepted.
          </p>
        )}
      </form>
    </div>
  );
}
