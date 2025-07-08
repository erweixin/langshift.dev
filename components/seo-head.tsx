'use client'

import { useEffect } from 'react'

interface SEOHeadProps {
  title?: string
  description?: string
  keywords?: string[]
  canonical?: string
  noindex?: boolean
}

export function SEOHead({
  title,
  description,
  keywords = [],
  canonical,
  noindex = false
}: SEOHeadProps) {
  useEffect(() => {
    // 动态更新页面标题
    if (title) {
      document.title = title
    }

    // 更新 meta 描述
    let metaDescription = document.querySelector('meta[name="description"]')
    if (!metaDescription) {
      metaDescription = document.createElement('meta')
      metaDescription.setAttribute('name', 'description')
      document.head.appendChild(metaDescription)
    }
    if (description) {
      metaDescription.setAttribute('content', description)
    }

    // 更新关键词
    let metaKeywords = document.querySelector('meta[name="keywords"]')
    if (!metaKeywords) {
      metaKeywords = document.createElement('meta')
      metaKeywords.setAttribute('name', 'keywords')
      document.head.appendChild(metaKeywords)
    }
    if (keywords.length > 0) {
      metaKeywords.setAttribute('content', keywords.join(', '))
    }

    // 设置 canonical 链接
    if (canonical) {
      let canonicalLink = document.querySelector('link[rel="canonical"]')
      if (!canonicalLink) {
        canonicalLink = document.createElement('link')
        canonicalLink.setAttribute('rel', 'canonical')
        document.head.appendChild(canonicalLink)
      }
      canonicalLink.setAttribute('href', canonical)
    }

    // 设置 robots
    if (noindex) {
      let metaRobots = document.querySelector('meta[name="robots"]')
      if (!metaRobots) {
        metaRobots = document.createElement('meta')
        metaRobots.setAttribute('name', 'robots')
        document.head.appendChild(metaRobots)
      }
      metaRobots.setAttribute('content', 'noindex, nofollow')
    }
  }, [title, description, keywords, canonical, noindex])

  return null
} 