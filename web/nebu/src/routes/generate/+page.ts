import { redirect } from '@sveltejs/kit';
import type { PageLoad } from './$types';

// Images and video are made on the chat page now; an old link keeps its model
export const load: PageLoad = ({ url }) => {
  const model = url.searchParams.get('model');
  redirect(307, model ? `/chat?model=${encodeURIComponent(model)}` : '/chat');
};
