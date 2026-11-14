import { Fragment, type ReactNode } from 'react';

// The pooltune logo: three bars plus the lowercase wordmark. Sized in em, so it scales
// with whatever text it sits in. Pass a className to set a fixed size (nav, footer).
export default function Wordmark({ className = '' }: { className?: string }) {
  return (
    <span className={`wordmark${className ? ` ${className}` : ''}`}>
      <span className="wordmark__mark" aria-hidden="true"><i /><i /><i /></span>
      <span className="wordmark__word">pooltune</span>
    </span>
  );
}

// Replaces each "Pooltune" in a sentence with the inline logo.
export function withBrand(text: string): ReactNode {
  const parts = text.split('Pooltune');
  if (parts.length === 1) return text;
  return parts.map((part, i) => (
    <Fragment key={i}>
      {i > 0 && <Wordmark />}
      {part}
    </Fragment>
  ));
}
