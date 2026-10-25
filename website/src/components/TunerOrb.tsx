import { ThinkingOrb } from 'thinking-orbs';

export type OrbMood = 'off' | 'idle' | 'working' | 'alert';

// thinking-orbs state for each mood in the scroll story.
const states = {
  off: 'breathing',
  idle: 'breathing',
  working: 'working',
  alert: 'solving',
} as const;

// Small status orb on the Pooltune box in the scroll story.
export default function TunerOrb({ mood }: { mood: OrbMood }) {
  return (
    <span className={`orb orb--${mood}`} aria-hidden="true">
      <ThinkingOrb state={states[mood]} size={64} theme="dark" paused={mood === 'off'} speed={mood === 'alert' ? 1.6 : 1} />
    </span>
  );
}
