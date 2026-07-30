import { createApp } from 'vue'
import { addCollection } from '@iconify/vue'
import solar from './solar-icons.json'
import App from './App.vue'
import './styles.css'

addCollection(solar)
createApp(App).mount('#app')
