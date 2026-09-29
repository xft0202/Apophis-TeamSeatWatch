import '@fontsource/dm-sans/400.css';
import '@fontsource/dm-sans/500.css';
import '@fontsource/dm-sans/700.css';
import { createRoot } from 'react-dom/client';
import PublicApp from './PublicApp';
import '../shared/app.css';

const root = document.getElementById('root');
if (!root) throw new Error('Public root element is missing');

createRoot(root).render(<PublicApp />);
