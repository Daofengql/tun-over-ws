import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import Container from '@mui/material/Container'
import Card from '@mui/material/Card'
import CardContent from '@mui/material/CardContent'
import Typography from '@mui/material/Typography'
import Button from '@mui/material/Button'
import Alert from '@mui/material/Alert'
import Stack from '@mui/material/Stack'
import Divider from '@mui/material/Divider'
import CircularProgress from '@mui/material/CircularProgress'
import { getAuthSession, approveAuthSession, type AuthSessionInfo } from '../api/client'

function errorMessage(err: unknown) {
  return err instanceof Error ? err.message : '请求失败'
}

export default function DeviceAuth() {
  const [searchParams] = useSearchParams()
  const code = searchParams.get('code')
  const [info, setInfo] = useState<AuthSessionInfo | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [approved, setApproved] = useState(false)

  useEffect(() => {
    let cancelled = false
    async function loadSession() {
      if (!code) {
        setError('缺少授权码')
        setLoading(false)
        return
      }
      try {
        const data = await getAuthSession(code)
        if (cancelled) return
        setInfo(data)
        setError('')
      } catch (err: unknown) {
        if (!cancelled) setError(errorMessage(err))
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void loadSession()
    return () => { cancelled = true }
  }, [code])

  const handleApprove = async () => {
    if (!code) return
    try {
      await approveAuthSession(code)
      setApproved(true)
    } catch (err: unknown) {
      setError(errorMessage(err))
    }
  }

  const parseDeviceInfo = (infoStr: string) => {
    try {
      const obj = JSON.parse(infoStr)
      return {
        os: obj.os || '-',
        arch: obj.arch || '-',
        hostname: obj.hostname || '-',
        machine_id: obj.machine_id || '-',
      }
    } catch {
      return { os: infoStr, arch: '-', hostname: '-', machine_id: '-' }
    }
  }

  if (loading) {
    return (
      <Container maxWidth="sm" sx={{ mt: 10, textAlign: 'center' }}>
        <CircularProgress />
      </Container>
    )
  }

  return (
    <Container maxWidth="sm" sx={{ mt: 10 }}>
      <Card>
        <CardContent>
          <Typography variant="h5" gutterBottom>设备授权请求</Typography>

          {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

          {approved && (
            <Alert severity="success" sx={{ mb: 2 }}>
              设备已授权成功！可以关闭此页面。客户端将自动完成连接。
            </Alert>
          )}

          {info && !approved && (
            <>
              <Stack spacing={1} sx={{ my: 2 }}>
                {(() => {
                  const di = parseDeviceInfo(info.device.device_info)
                  return (
                    <>
                      <Typography><strong>设备 ID:</strong> {info.device.device_id}</Typography>
                      <Typography><strong>设备名称:</strong> {info.device.name || '-'}</Typography>
                      <Typography><strong>系统:</strong> {di.os} / {di.arch}</Typography>
                      <Typography><strong>主机名:</strong> {di.hostname}</Typography>
                      <Typography><strong>Machine ID:</strong> {di.machine_id}</Typography>
                      <Typography><strong>状态:</strong> {info.session.status}</Typography>
                    </>
                  )
                })()}
              </Stack>

              <Divider sx={{ my: 2 }} />

              {info.session.status === 'pending' ? (
                <Stack direction="row" spacing={2}>
                  <Button variant="contained" color="primary" onClick={handleApprove} fullWidth>
                    授权此设备
                  </Button>
                </Stack>
              ) : (
                <Alert severity="info">此授权请求已处理: {info.session.status}</Alert>
              )}
            </>
          )}
        </CardContent>
      </Card>
    </Container>
  )
}
