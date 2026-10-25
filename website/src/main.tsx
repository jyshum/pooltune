import { StrictMode, useEffect } from 'react';
import { createRoot } from 'react-dom/client';
import '@fontsource/nunito/latin-800.css';
import App from './App';
import SetupPage from './SetupPage';
import './styles.css';

function Page() {
  // Anchor targets do not exist until React mounts the static page.
  useEffect(() => {
    const frame = requestAnimationFrame(() => {
      document.getElementById(window.location.hash.slice(1))?.scrollIntoView();
    });
    return () => cancelAnimationFrame(frame);
  }, []);
  return window.location.pathname.endsWith('/setup.html') ? <SetupPage /> : <App />;
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Page />
  </StrictMode>,
);
