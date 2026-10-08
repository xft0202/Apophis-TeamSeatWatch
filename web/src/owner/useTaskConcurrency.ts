import { useEffect, useState } from 'react';
import createClient from 'openapi-fetch';
import type { paths } from '../generated/owner';

const api = createClient<paths>({ baseUrl: '', credentials: 'include' });

// Shared task controls read the same database ceiling as the runtime lease manager.
export default function useTaskConcurrency(active: boolean) {
	const [limit, setLimit] = useState(1);
	const [requested, setRequested] = useState(1);
	const [ready, setReady] = useState(false);
	useEffect(() => {
		if (!active) return;
		const controller = new AbortController();
		setReady(false);
		void api.GET('/api/owner/v1/proxy-pool/settings', { signal: controller.signal }).then(({ data }) => {
			if (controller.signal.aborted || !data) return;
			setLimit(data.taskConcurrency); setRequested(value => Math.min(value, data.taskConcurrency)); setReady(true);
		}).catch(() => { /* The control remains disabled until the persisted ceiling can be read. */ });
		return () => controller.abort();
	}, [active]);
	return { limit, concurrency: Math.max(1, Math.min(requested, limit)), ready, setConcurrency: (value: number | string) => setRequested(Math.max(1, Math.min(Number(value) || 1, limit))) };
}
