/*
Copyright (C) 2025 QuantumNous

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
import React, { useEffect } from 'react';
import { useTokenKeys } from '../../hooks/chat/useTokenKeys';

const IMAGE_SITE_URL = 'https://image.1omgt.app';

const Image2Link = () => {
  const { keys } = useTokenKeys();

  useEffect(() => {
    if (!keys.length) return;

    const url = new URL(IMAGE_SITE_URL);
    const params = new URLSearchParams({
      baseUrl: window.location.origin,
      apiKey: `sk-${keys[0]}`,
    });
    url.hash = `newapi?${params.toString()}`;
    window.location.href = url.toString();
  }, [keys]);

  return (
    <div className='mt-[60px] px-2'>
      <h3>正在打开生图站，请稍候...</h3>
    </div>
  );
};

export default Image2Link;
