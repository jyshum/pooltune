import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '@fontsource/nunito/latin-800.css';
import App from './App';
import SetupPage from './SetupPage';
import './styles.css';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    {window.location.pathname.endsWith('/setup.html') ? <SetupPage /> : <App />}
  </StrictMode>,
);
