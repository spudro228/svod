import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './tokens.css'
import './app.css'
import App from './App'

const root = document.getElementById('root')
if (!root) throw new Error('нет элемента #root')

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
