import '@fontsource/dm-sans/400.css';
import '@fontsource/dm-sans/500.css';
import '@fontsource/dm-sans/700.css';
import '@fontsource/jetbrains-mono/400.css';
import { createRoot } from 'react-dom/client';
import OwnerApp from './OwnerApp';
import '../shared/app.css';

const root = document.getElementById('root');
if (!root) throw new Error('Owner root element is missing');

createRoot(root).render(<OwnerApp />);
