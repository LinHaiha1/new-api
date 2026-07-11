/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Loader2 } from 'lucide-react'
import { useEffect } from 'react'
import { toast } from 'sonner'

import { useActiveChatKey } from '@/features/chat/hooks/use-active-chat-key'

const IMAGE_SITE_URL = 'https://image.1omgt.com'

export const Route = createFileRoute('/_authenticated/image2link')({
  component: Image2LinkPage,
})

function Image2LinkPage() {
  const navigate = useNavigate()
  const { data: activeKey, error: keyError } = useActiveChatKey(true)

  useEffect(() => {
    if (activeKey === undefined && !keyError) return

    if (keyError || !activeKey) {
      const message =
        keyError instanceof Error
          ? keyError.message
          : 'No enabled tokens available'
      toast.error(message)
      navigate({ to: '/keys' })
      return
    }

    const url = new URL(IMAGE_SITE_URL)
    const params = new URLSearchParams({
      baseUrl: window.location.origin,
      apiKey: activeKey,
    })
    url.hash = `newapi?${params.toString()}`
    window.location.href = url.toString()
  }, [activeKey, keyError, navigate])

  return (
    <div className='flex h-full flex-col items-center justify-center gap-3'>
      <Loader2 className='text-muted-foreground h-8 w-8 animate-spin' />
      <p className='text-muted-foreground text-sm'>正在打开生图站...</p>
    </div>
  )
}
