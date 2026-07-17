import { createApp } from 'vue';
import App from './App.vue';
import { latencyStore } from './stores/latency';

const app = createApp(App);

// Surface failures instead of silently leaving a blank panel: a render or
// charting error otherwise only shows up in the devtools console.
app.config.errorHandler = (err) => {
  latencyStore.state.error = 'vue: ' + String(err);
  console.error(err);
};
window.addEventListener('error', (e) => {
  latencyStore.state.error = 'js: ' + e.message + ' @' + e.filename + ':' + e.lineno;
});
window.addEventListener('unhandledrejection', (e) => {
  latencyStore.state.error = 'promise: ' + String(e.reason);
});

app.mount('#app');
latencyStore.start();
