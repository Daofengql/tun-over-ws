import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import Container from '@mui/material/Container'
import Box from '@mui/material/Box'
import Typography from '@mui/material/Typography'
import Button from '@mui/material/Button'
import Paper from '@mui/material/Paper'
import Stack from '@mui/material/Stack'
import Alert from '@mui/material/Alert'
import Chip from '@mui/material/Chip'
import type { ChipProps } from '@mui/material/Chip'
import CircularProgress from '@mui/material/CircularProgress'
import ArrowBackIcon from '@mui/icons-material/ArrowBack'
import CheckCircleIcon from '@mui/icons-material/CheckCircle'
import BlockIcon from '@mui/icons-material/Block'
import { approveDevice, getDevice, revokeDevice, type Device } from '../api/client'

const statusColor = (s: string): ChipProps['color'] => {
  if (s === 'approved') return 'success'
  if (s === 'pending') return 'warning'
  return 'error'
}

interface ParsedDeviceInfo {
  raw?: string;
  os?: string;
  arch?: string;
  hostname?: string;
  machine_id?: string;
}

function errorMessage(err: unknown) {
  return err instanceof Error ? err.message : '请求失败'
}

const statusLabel = (s: string) => {
  if (s === 'approved') return '已授权'
  if (s === 'pending') return '待授权'
  return '已吊销'
}

function parseDeviceInfo(info: string): ParsedDeviceInfo {
  try {
    return JSON.parse(info)
  } catch {
    return { raw: info }
  }
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <Box sx={{ display: 'grid', gridTemplateColumns: '140px 1fr', gap: 2 }}>
      <Typography color="text.secondary">{label}</Typography>
      <Typography sx={{ overflowWrap: 'anywhere' }}>{value || '-'}</Typography>
    </Box>
  )
}

export default function DeviceDetail() {
  const { deviceID = '' } = useParams()
  const navigate = useNavigate()
  const [device, setDevice] = useState<Device | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const load = async () => {
    try {
      setLoading(true)
      setDevice(await getDevice(deviceID))
      setError('')
    } catch (err: unknown) {
      const message = errorMessage(err)
      if (message === 'unauthorized') { navigate('/admin/login'); return }
      setError(message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    let cancelled = false
    async function loadInitial() {
      try {
        setLoading(true)
        const data = await getDevice(deviceID)
        if (cancelled) return
        setDevice(data)
        setError('')
      } catch (err: unknown) {
        if (cancelled) return
        const message = errorMessage(err)
        if (message === 'unauthorized') { navigate('/admin/login'); return }
        setError(message)
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void loadInitial()
    return () => { cancelled = true }
  }, [navigate, deviceID])

  const setApproved = async () => {
    await approveDevice(deviceID)
    load()
  }

  const setRevoked = async () => {
    await revokeDevice(deviceID)
    load()
  }

  const info = device ? parseDeviceInfo(device.device_info) : {}

  return (
    <Container maxWidth="md" sx={{ mt: 4 }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', mb: 3 }}>
        <Button startIcon={<ArrowBackIcon />} onClick={() => navigate('/admin/dashboard')}>返回</Button>
        {device && (
          <Box sx={{ display: 'flex', gap: 1 }}>
            {device.status === 'pending' && (
              <Button startIcon={<CheckCircleIcon />} variant="contained" color="success" onClick={setApproved}>授权</Button>
            )}
            {device.status === 'approved' && (
              <Button startIcon={<BlockIcon />} variant="outlined" color="warning" onClick={setRevoked}>吊销</Button>
            )}
          </Box>
        )}
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      {loading ? (
        <Box sx={{ display: 'flex', justifyContent: 'center', py: 8 }}><CircularProgress /></Box>
      ) : device && (
        <Paper sx={{ p: 3 }}>
          <Stack spacing={2}>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
              <Typography variant="h5">{device.name || '未命名设备'}</Typography>
              <Chip label={statusLabel(device.status)} color={statusColor(device.status)} size="small" />
            </Box>
            <Row label="设备 ID" value={device.device_id} />
            <Row label="虚拟 IP" value={device.virtual_ip || device.auto_vip} />
            <Row label="自动分配 IP" value={device.auto_vip} />
            <Row label="自定义 IP" value={device.virtual_ip || ''} />
            <Row label="系统" value={info.raw ? info.raw : `${info.os || '-'} / ${info.arch || '-'}`} />
            <Row label="主机名" value={info.hostname || ''} />
            <Row label="Machine ID" value={info.machine_id || ''} />
            <Row label="AK 过期" value={device.key_expires_at ? new Date(device.key_expires_at).toLocaleString() : ''} />
            <Row label="最后在线" value={device.last_seen_at ? new Date(device.last_seen_at).toLocaleString() : '从未'} />
            <Row label="创建时间" value={new Date(device.created_at).toLocaleString()} />
            <Row label="更新时间" value={new Date(device.updated_at).toLocaleString()} />
          </Stack>
        </Paper>
      )}
    </Container>
  )
}
