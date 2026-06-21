import { useState, type FormEvent } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import Container from '@mui/material/Container'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import TextField from '@mui/material/TextField'
import Button from '@mui/material/Button'
import Typography from '@mui/material/Typography'
import Alert from '@mui/material/Alert'
import Stack from '@mui/material/Stack'
import { useAuth } from '../auth/useAuth'

function errorMessage(err: unknown, fallback: string) {
  return err instanceof Error ? err.message : fallback
}

export default function Login() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const { login } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      await login(username, password)
      const from = (location.state as { from?: string } | null)?.from || '/admin/dashboard'
      navigate(from)
    } catch (err: unknown) {
      setError(errorMessage(err, '登录失败'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Container maxWidth="sm" sx={{ mt: 10 }}>
      <Card>
        <CardContent>
          <Typography variant="h5" gutterBottom>WSVPN 管理控制台</Typography>
          {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}
          <form onSubmit={handleSubmit}>
            <Stack spacing={2}>
              <TextField
                label="用户名"
                value={username}
                onChange={e => setUsername(e.target.value)}
                fullWidth
                autoFocus
              />
              <TextField
                label="密码"
                type="password"
                value={password}
                onChange={e => setPassword(e.target.value)}
                fullWidth
              />
              <Button type="submit" variant="contained" disabled={loading} fullWidth>
                {loading ? '登录中...' : '登录'}
              </Button>
            </Stack>
          </form>
        </CardContent>
      </Card>
    </Container>
  )
}
