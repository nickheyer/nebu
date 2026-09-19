import { redirect } from '@sveltejs/kit';
import type { PageLoad } from './$types';

// Preserve the model when redirecting old generation links to chat.
export const load: PageLoad = ({ url }) => {
  const model = url.searchParams.get('model');
  redirect(307, model ? `/chat?model=${encodeURIComponent(model)}` : '/chat');
};
