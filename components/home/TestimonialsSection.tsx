import { Star } from 'lucide-react';
import { type SupportedLanguage } from '@/messages';

interface TestimonialsSectionProps {
  lang: SupportedLanguage;
  translations: any;
}

export function TestimonialsSection({ lang, translations }: TestimonialsSectionProps) {
  const t = translations;

  return (
    <div className="py-20">
      <div className="container mx-auto px-4">
        <div className="text-center mb-16">
          <h2 className="text-4xl md:text-5xl font-bold text-white mb-6">
            {t.home.testimonials.title}
          </h2>
          <p className="text-xl text-slate-400 max-w-3xl mx-auto">
            {t.home.testimonials.subtitle}
          </p>
        </div>

        <div className="grid md:grid-cols-3 gap-8 max-w-6xl mx-auto">
          {t.home.testimonials.items.map((testimonial: any, index: number) => (
            <div key={index} className="bg-slate-800/30 backdrop-blur-sm border border-slate-700/50 rounded-2xl p-8 hover:scale-105 transition-all duration-300">
              <div className="flex items-center mb-6">
                <div className="w-14 h-14 bg-gradient-to-br from-blue-500 to-purple-600 rounded-full flex items-center justify-center text-2xl mr-4">
                  {testimonial.avatar}
                </div>
                <div>
                  <h4 className="text-lg font-semibold text-white">{testimonial.name}</h4>
                  <p className="text-slate-400 text-sm">{testimonial.role}</p>
                  <div className="flex items-center gap-1 mt-1">
                    {[...Array(5)].map((_, i) => (
                      <Star key={i} className="w-4 h-4 fill-yellow-400 text-yellow-400" />
                    ))}
                  </div>
                </div>
              </div>
              <p className="text-slate-300 leading-relaxed italic">
                &ldquo;{testimonial.content}&rdquo;
              </p>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
} 